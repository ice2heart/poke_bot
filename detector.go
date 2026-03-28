package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// detectorCh receives all updates for spam analysis.
var detectorCh = make(chan *models.Update, 128)

const reactionCacheSize = 20

type reactionEntry struct {
	userID   int64
	username string
	emoji    string
}

var (
	reactionCacheMux sync.Mutex
	reactionCache    = make(map[int64][]reactionEntry) // chatID -> entries
)

func pushReactionEntry(chatID int64, entry reactionEntry) {
	reactionCacheMux.Lock()
	defer reactionCacheMux.Unlock()
	entries := reactionCache[chatID]
	entries = append(entries, entry)
	if len(entries) > reactionCacheSize {
		entries = entries[len(entries)-reactionCacheSize:]
	}
	reactionCache[chatID] = entries
}

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

func processDetectorReaction(update *models.Update) {
	r := update.MessageReaction
	userID, username, newEmojis := extractNewEmojis(r)
	if len(newEmojis) == 0 {
		return
	}
	log.Printf("[detector] reaction: userID=%d username=%q emojis=%v chatID=%d messageID=%d",
		userID, username, newEmojis, r.Chat.ID, r.MessageID)
	for _, emoji := range newEmojis {
		pushReactionEntry(r.Chat.ID, reactionEntry{
			userID:   userID,
			username: username,
			emoji:    emoji,
		})
	}
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
				processDetectorReaction(update)
			}
		}
	}
}

func processDetectorEdit(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.MessageReaction != nil {
		return
	}
	msg := update.EditedMessage

	editDelaySec := msg.EditDate - msg.Date
	if editDelaySec <= editLinkMinDelay {
		return
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

	log.Printf("[detector] suspected spam edit: messageID=%d chatID=%d editDelaySec=%d",
		msg.ID, msg.Chat.ID, editDelaySec)

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

func likesHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID

	reactionCacheMux.Lock()
	entries := make([]reactionEntry, len(reactionCache[chatID]))
	copy(entries, reactionCache[chatID])
	reactionCacheMux.Unlock()

	if len(entries) == 0 {
		systemMessage(ctx, b, chatID, "Реакций пока нет", 30)
		return
	}

	rows := make([]string, 0, len(entries)+2)
	rows = append(rows, "Пользователь \\| Реакция \\| Ссылка")
	for _, e := range entries {
		name := e.username
		if name == "" {
			name = fmt.Sprintf("id%d", e.userID)
		}
		rows = append(rows, fmt.Sprintf("%s \\| %s \\| tg://user?id\\=%d", escape(name), e.emoji, e.userID))
	}

	systemMessage(ctx, b, chatID, strings.Join(rows, "\n"), 5*60)
}

// detectorMiddleware feeds all message and edit updates into the detection goroutine.
func detectorMiddleware(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		select {
		case detectorCh <- update:
		default:
			log.Printf("[detector] channel full, dropping update")
		}
		next(ctx, b, update)
	}
}
