package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/ice2heart/poke_bot/cache"
	"go.uber.org/zap"
)

// detectorCh receives all updates for spam analysis.
var detectorCh = make(chan *models.Update, 128)

const reactionTTL = 24 * time.Hour

type reactionKey struct {
	chatID int64
	userID int64
}

type reactionEntry struct {
	userID      int64
	username    string
	altUsername string
	emoji       string
}

var reactionCache *cache.Cache[reactionKey, reactionEntry]

// extractNewEmojis returns emojis added in NewReaction that were not in OldReaction.
func extractNewEmojis(r *models.MessageReactionUpdated) (userID int64, username string, emojis []string) {
	if r.User != nil {
		userID = r.User.ID
		username = r.User.Username
		if username == "" {
			username = strings.TrimSpace(r.User.FirstName + " " + r.User.LastName)
		}
	}
	oldSet := make(map[string]bool, len(r.OldReaction))
	for _, rt := range r.OldReaction {
		if rt.Type == models.ReactionTypeTypeEmoji && rt.ReactionTypeEmoji != nil {
			oldSet[rt.ReactionTypeEmoji.Emoji] = true
		}
	}
	for _, rt := range r.NewReaction {
		if rt.Type == models.ReactionTypeTypeEmoji && rt.ReactionTypeEmoji != nil {
			if !oldSet[rt.ReactionTypeEmoji.Emoji] {
				emojis = append(emojis, rt.ReactionTypeEmoji.Emoji)
			}
		}
	}
	return
}

func processDetectorReaction(ctx context.Context, update *models.Update) {
	r := update.MessageReaction
	userID, username, newEmojis := extractNewEmojis(r)
	if len(newEmojis) == 0 {
		return
	}
	zap.S().Infof("[detector] reaction: userID=%d username=%q emojis=%v chatID=%d messageID=%d",
		userID, username, newEmojis, r.Chat.ID, r.MessageID)

	// Ensure the reacting user exists in the users collection so they can be
	// ban-targeted later.
	var altUsername string
	if resolved, err := resolveUser(ctx, userID); err == nil {
		zap.S().Infof("[detector] resolved userID=%d username=%q altUsername=%q", userID, resolved.Username, resolved.AltUsername)
		username = resolved.Username
		altUsername = resolved.AltUsername
	} else {
		zap.S().Infof("[detector] resolveUser failed for userID=%d: %v; falling back to update data", userID, err)
		if r.User != nil {
			altUsername = strings.TrimSpace(r.User.FirstName + " " + r.User.LastName)
			if err := ensureUser(ctx, userID, r.User.Username, altUsername); err != nil {
				zap.S().Infof("[detector] ensureUser fallback failed for userID=%d: %v", userID, err)
			}
		}
	}

	// Use the last emoji if multiple new ones; one entry per user per chat.
	emoji := newEmojis[len(newEmojis)-1]

	go saveReaction(ctx, &ReactionRecord{
		UserID:    userID,
		ChatID:    r.Chat.ID,
		MessageID: r.MessageID,
		Emoji:     emoji,
		Date:      int64(r.Date),
	})

	reactionCache.Set(reactionKey{chatID: r.Chat.ID, userID: userID}, reactionEntry{
		userID:      userID,
		username:    username,
		altUsername: altUsername,
		emoji:       emoji,
	}, reactionTTL)
}

// editLinkMinDelay is the minimum edit delay (seconds) that triggers link-spam detection.
const editLinkMinDelay = 120 // 2 minutes

// startDetector runs the detection loop in a goroutine; exits when ctx is cancelled.
func startDetector(ctx context.Context, b *bot.Bot) {
	for {
		select {
		case <-ctx.Done():
			return
		case update := <-detectorCh:
			// if data, err := json.MarshalIndent(update, "", "\t"); err == nil {
			// 	log.Printf("[startDetector] update: %s", data)
			// }
			if update.EditedMessage != nil {
				processDetectorEdit(ctx, b, update)
			}
			if update.MessageReaction != nil {
				processDetectorReaction(ctx, update)
			}
		}
	}
}

func processDetectorEdit(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.MessageReaction != nil {
		return
	}
	msg := update.EditedMessage
	if msg == nil || msg.SenderTag != "" {
		return
	}

	if msg.From != nil {
		adminsMux.Lock()
		chatAdmins := checkAdmins(ctx, b, msg.Chat.ID)
		adminsMux.Unlock()
		if _, isAdmin := chatAdmins[msg.From.ID]; isAdmin {
			return
		}
	}

	settingsMux.Lock()
	chatSettings := getChatSettings(ctx, msg.Chat.ID)
	settingsMux.Unlock()
	if chatSettings.LinkedChannelUsername != "" && msg.SenderChat != nil &&
		strings.EqualFold(msg.SenderChat.Username, chatSettings.LinkedChannelUsername) {
		return
	}

	editDelaySec := msg.EditDate - msg.Date
	if editDelaySec <= editLinkMinDelay {
		return
	}

	for _, e := range msg.Entities {
		if e.Type == models.MessageEntityTypeBotCommand {
			return
		}
	}

	hasLink := false
	for _, e := range msg.Entities {
		if e.Type == models.MessageEntityTypeURL || e.Type == models.MessageEntityTypeTextLink {
			hasLink = true
			break
		}
	}
	if !hasLink {
		for _, e := range msg.CaptionEntities {
			if e.Type == models.MessageEntityTypeURL || e.Type == models.MessageEntityTypeTextLink {
				hasLink = true
				break
			}
		}
	}
	if !hasLink {
		return
	}

	zap.S().Infof("[detector] suspected spam edit: messageID=%d chatID=%d editDelaySec=%d",
		msg.ID, msg.Chat.ID, editDelaySec)
	if data, err := json.MarshalIndent(update, "", "\t"); err == nil {
		zap.S().Infof("[detector] update dump: %s", data)
	}

	updatedText := msg.Text
	if updatedText == "" {
		updatedText = msg.Caption
	}

	msgLink := fmt.Sprintf("tg://privatepost?channel=%s&post=%d",
		makePublicGroupString(msg.Chat.ID), msg.ID)
	text := fmt.Sprintf(
		"Подозрительная активность: [сообщение](%s) отредактировано с добавлением ссылки спустя %d с\n\n%s",
		msgLink, editDelaySec, quoteText(updatedText),
	)

	systemMessage(ctx, b, msg.Chat.ID, text, 5*60)
}

const likesTopN = 10

// renderLikesPage builds the text and nav keyboard for one page of the
// reaction leaderboard. page is 0-indexed.
func renderLikesPage(ctx context.Context, chatID int64, page int) (text string, kb *models.InlineKeyboardMarkup) {
	entries := reactionCache.FilterSorted(func(k reactionKey) bool {
		return k.chatID == chatID
	})

	if len(entries) == 0 {
		return "Реакций пока нет", nil
	}

	pageCount := (len(entries) + likesTopN - 1) / likesTopN
	if page < 0 {
		page = 0
	}
	if page > pageCount-1 {
		page = pageCount - 1
	}

	start := page * likesTopN
	end := start + likesTopN
	if end > len(entries) {
		end = len(entries)
	}

	rows := make([]string, 0, end-start+1)
	rows = append(rows, "@username \\| Имя \\| Реакция \\| Сообщений \\| Ссылка")
	for _, e := range entries[start:end] {
		handle := escape(e.username)
		if handle == "" {
			handle = "—"
		}
		displayName := escape(e.altUsername)
		if displayName == "" {
			displayName = "—"
		}
		messageCount := "—"
		if user, err := getUser(ctx, e.userID); err == nil {
			messageCount = fmt.Sprintf("%d", user.Counter)
		}
		rows = append(rows, fmt.Sprintf("%s \\| %s \\| %s \\| %s \\| tg://user?id\\=%d", handle, displayName, e.emoji, messageCount, e.userID))
	}
	rows = append(rows, fmt.Sprintf("Страница %d/%d", page+1, pageCount))

	text = strings.Join(rows, "\n")
	kb = getLikesKeyboard(chatID, page, page > 0, page < pageCount-1)
	return text, kb
}

func likesHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	msgID := update.Message.ID

	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    chatID,
		MessageID: msgID,
	})

	text, kb := renderLikesPage(ctx, chatID, 0)

	sent, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   models.ParseModeMarkdown,
		ReplyMarkup: kb,
	})
	if err != nil {
		zap.S().Infof("[likesHandler] SendMessage failed chatID=%d: %v", chatID, err)
		return
	}
	sentID := sent.ID
	go delay(ctx, 5*60, func() {
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    chatID,
			MessageID: sentID,
		})
	})
}

// detectorMiddleware feeds all message and edit updates into the detection goroutine.
func detectorMiddleware(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		select {
		case detectorCh <- update:
		default:
			zap.S().Info("[detector] channel full, dropping update")
		}
		next(ctx, b, update)
	}
}
