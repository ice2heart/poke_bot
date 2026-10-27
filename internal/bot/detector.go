package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/ice2heart/poke_bot/internal/storage"
	"github.com/ice2heart/poke_bot/internal/tgutil"
	"go.uber.org/zap"
)

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

func (a *App) processDetectorReaction(ctx context.Context, update *models.Update) {
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
	if resolved, err := a.resolveUser(ctx, userID); err == nil {
		zap.S().Infof("[detector] resolved userID=%d username=%q altUsername=%q", userID, resolved.Username, resolved.AltUsername)
		username = resolved.Username
		altUsername = resolved.AltUsername
	} else {
		zap.S().Infof("[detector] resolveUser failed for userID=%d: %v; falling back to update data", userID, err)
		if r.User != nil {
			altUsername = strings.TrimSpace(r.User.FirstName + " " + r.User.LastName)
			if err := storage.EnsureUser(ctx, userID, r.User.Username, altUsername); err != nil {
				zap.S().Infof("[detector] ensureUser fallback failed for userID=%d: %v", userID, err)
			}
		}
	}

	// Use the last emoji if multiple new ones; one entry per user per chat.
	emoji := newEmojis[len(newEmojis)-1]

	go storage.SaveReaction(ctx, &storage.ReactionRecord{
		UserID:    userID,
		ChatID:    r.Chat.ID,
		MessageID: r.MessageID,
		Emoji:     emoji,
		Date:      int64(r.Date),
	})

	a.reactionCache.Set(reactionKey{chatID: r.Chat.ID, userID: userID}, reactionEntry{
		userID:      userID,
		username:    username,
		altUsername: altUsername,
		emoji:       emoji,
	}, reactionTTL)
}

// editLinkMinDelay is the minimum edit delay (seconds) that triggers link-spam detection.
const editLinkMinDelay = 120 // 2 minutes

// patternCache holds the compiled form of every ban pattern seen so far.
type patternCache struct {
	mu sync.Mutex
	m  map[string]*regexp.Regexp
}

func newPatternCache() *patternCache {
	return &patternCache{m: make(map[string]*regexp.Regexp)}
}

// compiled returns the case-insensitive regexp for a stored ban pattern,
// compiling it once and caching the result. Returns nil for invalid patterns.
func (c *patternCache) compiled(pattern string) *regexp.Regexp {
	c.mu.Lock()
	defer c.mu.Unlock()
	re, ok := c.m[pattern]
	if !ok {
		var err error
		re, err = regexp.Compile("(?i)" + pattern)
		if err != nil {
			zap.S().Infof("[compiledPattern] invalid pattern %q: %v", pattern, err)
			re = nil
		}
		c.m[pattern] = re
	}
	return re
}

// chatBanPatterns returns a copy of the chat's ban patterns without creating a
// settings record for chats (e.g. private ones) that have none.
func (a *App) chatBanPatterns(chatID int64) (patterns []string, paused bool) {
	a.settingsMux.Lock()
	defer a.settingsMux.Unlock()
	chatSettings, ok := a.settings[chatID]
	if !ok {
		return nil, false
	}
	return slices.Clone(chatSettings.BanPatterns), chatSettings.Pause
}

// processDetectorMessage checks a new or edited message against the chat's ban
// patterns and automatically starts a ban vote when one matches.
func (a *App) processDetectorMessage(ctx context.Context, b *bot.Bot, msg *models.Message) {
	if msg == nil || msg.From == nil || msg.Chat.ID >= 0 || msg.From.ID == b.ID() {
		return
	}

	// Match against the same composite text that gets logged: text or caption
	// plus sticker/GIF descriptions and hidden text-link URLs.
	text := buildStoredText(msg)
	if text == "" {
		return
	}

	// Commands are handled by their own handlers.
	for _, e := range msg.Entities {
		if e.Type == models.MessageEntityTypeBotCommand && e.Offset == 0 {
			return
		}
	}

	patterns, paused := a.chatBanPatterns(msg.Chat.ID)
	if paused || len(patterns) == 0 {
		return
	}

	matched := ""
	for _, p := range patterns {
		if re := a.patterns.compiled(p); re != nil && re.MatchString(text) {
			matched = p
			break
		}
	}
	if matched == "" {
		return
	}

	userID := msg.From.ID
	if msg.SenderChat != nil {
		userID = msg.SenderChat.ID
	}

	a.adminsMux.Lock()
	_, isAdmin := a.checkAdmins(ctx, b, msg.Chat.ID)[userID]
	a.adminsMux.Unlock()
	if isAdmin {
		return
	}

	a.sessionsMux.Lock()
	running, _, _ := a.findSessionByUser(msg.Chat.ID, userID)
	a.sessionsMux.Unlock()
	if running != nil || a.banCache.getCachedBanInfo(msg.Chat.ID, userID) {
		return
	}

	zap.S().Infof("[detector] ban pattern %q matched: userID=%d chatID=%d messageID=%d",
		matched, userID, msg.Chat.ID, msg.ID)

	banInfo, err := a.getBanInfoByUserID(ctx, msg.Chat.ID, userID)
	if err != nil {
		zap.S().Infof("[detector] getBanInfoByUserID failed for userID=%d chatID=%d: %v", userID, msg.Chat.ID, err)
		return
	}
	banInfo.TargetMessageID = int64(msg.ID)
	banInfo.LastMessage = text
	banInfo.BanMessage = makeBanMessage(banInfo)
	// The bot itself owns automatic votes. There is no request message: the
	// vote stands alone (linked to the target message in its text), so a
	// cancelled or expired vote leaves the triggering message in place and
	// only a passed ban deletes it.
	banInfo.OwnerID = b.ID()

	a.makeVoteMessage(ctx, banInfo, b)
}

// patternQuotes maps opening quote characters to their closing counterparts.
var patternQuotes = map[rune]rune{'"': '"', '«': '»', '“': '”', '„': '“'}

var whitespaceRun = regexp.MustCompile(`\s+`)

// parsePatternArg turns the /add_pattern argument into a stored regexp. A
// phrase wrapped in quotes is matched literally (whitespace runs are relaxed to
// \s+); anything else is used as a regexp as is.
func parsePatternArg(arg string) string {
	arg = strings.TrimSpace(arg)
	runes := []rune(arg)
	if len(runes) < 3 {
		return arg
	}
	closing, ok := patternQuotes[runes[0]]
	if !ok || runes[len(runes)-1] != closing {
		return arg
	}
	phrase := strings.TrimSpace(string(runes[1 : len(runes)-1]))
	if phrase == "" {
		return arg
	}
	return whitespaceRun.ReplaceAllString(regexp.QuoteMeta(phrase), `\s+`)
}

// addPatternHandler adds a ban-trigger regexp to the chat settings.
// Usage: /add_pattern <regex> or /add_pattern "literal phrase"
func (a *App) addPatternHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	msgID := update.Message.ID
	if !a.isUserAdmin(ctx, b, chatID, update.Message.From.ID, chatID, msgID, ephemeralFor(update.Message)) {
		return
	}

	var pattern string
	if parts := strings.SplitN(update.Message.Text, " ", 2); len(parts) == 2 {
		pattern = parsePatternArg(parts[1])
	}
	if pattern == "" {
		systemAnswerToMessage(ctx, b, chatID, msgID, tgutil.Escape("Использование: /add_pattern <регулярное выражение> или /add_pattern \"точная фраза\""), true, 30, ephemeralFor(update.Message))
		return
	}
	if _, err := regexp.Compile("(?i)" + pattern); err != nil {
		systemAnswerToMessage(ctx, b, chatID, msgID, tgutil.Escape(fmt.Sprintf("Некорректное регулярное выражение: %v", err)), true, 30, ephemeralFor(update.Message))
		return
	}

	a.settingsMux.Lock()
	chatSettings := a.getChatSettings(ctx, chatID)
	exists := slices.Contains(chatSettings.BanPatterns, pattern)
	if !exists {
		chatSettings.BanPatterns = append(chatSettings.BanPatterns, pattern)
		storage.WriteChatSettings(ctx, chatID, chatSettings)
	}
	a.settingsMux.Unlock()

	if exists {
		systemAnswerToMessage(ctx, b, chatID, msgID, tgutil.Escape("Такой паттерн уже добавлен"), true, 30, ephemeralFor(update.Message))
		return
	}
	zap.S().Infof("[addPatternHandler] chatID=%d pattern=%q added by userID=%d", chatID, pattern, update.Message.From.ID)
	systemAnswerToMessage(ctx, b, chatID, msgID, tgutil.Escape(fmt.Sprintf("Паттерн добавлен: %s", pattern)), true, 30, ephemeralFor(update.Message))
}

// delPatternHandler removes a ban-trigger regexp by its 1-based index.
// Called without a valid index it lists the configured patterns.
// Usage: /del_pattern <номер>
func (a *App) delPatternHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	msgID := update.Message.ID
	if !a.isUserAdmin(ctx, b, chatID, update.Message.From.ID, chatID, msgID, ephemeralFor(update.Message)) {
		return
	}

	a.settingsMux.Lock()
	chatSettings := a.getChatSettings(ctx, chatID)
	patterns := slices.Clone(chatSettings.BanPatterns)
	a.settingsMux.Unlock()

	if len(patterns) == 0 {
		systemAnswerToMessage(ctx, b, chatID, msgID, tgutil.Escape("Паттерны не настроены"), true, 30, ephemeralFor(update.Message))
		return
	}

	var index int
	if parts := strings.SplitN(update.Message.Text, " ", 2); len(parts) == 2 {
		index, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
	}
	if index < 1 || index > len(patterns) {
		lines := make([]string, 0, len(patterns)+1)
		lines = append(lines, "Использование: /del_pattern <номер>")
		for i, p := range patterns {
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, p))
		}
		systemAnswerToMessage(ctx, b, chatID, msgID, tgutil.Escape(strings.Join(lines, "\n")), true, 60, ephemeralFor(update.Message))
		return
	}

	removed := patterns[index-1]

	a.settingsMux.Lock()
	chatSettings = a.getChatSettings(ctx, chatID)
	if i := slices.Index(chatSettings.BanPatterns, removed); i >= 0 {
		chatSettings.BanPatterns = slices.Delete(chatSettings.BanPatterns, i, i+1)
		storage.WriteChatSettings(ctx, chatID, chatSettings)
	}
	a.settingsMux.Unlock()

	zap.S().Infof("[delPatternHandler] chatID=%d pattern=%q removed by userID=%d", chatID, removed, update.Message.From.ID)
	systemAnswerToMessage(ctx, b, chatID, msgID, tgutil.Escape(fmt.Sprintf("Паттерн удалён: %s", removed)), true, 30, ephemeralFor(update.Message))
}

// startDetector runs the detection loop in a goroutine; exits when ctx is cancelled.
func (a *App) startDetector(ctx context.Context, b *bot.Bot) {
	for {
		select {
		case <-ctx.Done():
			return
		case update := <-a.detectorCh:
			// if data, err := json.MarshalIndent(update, "", "\t"); err == nil {
			// 	log.Printf("[startDetector] update: %s", data)
			// }
			if update.Message != nil {
				a.processDetectorMessage(ctx, b, update.Message)
			}
			if update.EditedMessage != nil {
				a.processDetectorMessage(ctx, b, update.EditedMessage)
				a.processDetectorEdit(ctx, b, update)
			}
			if update.MessageReaction != nil {
				a.processDetectorReaction(ctx, update)
			}
		}
	}
}

func (a *App) processDetectorEdit(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.MessageReaction != nil {
		return
	}
	msg := update.EditedMessage
	if msg == nil || msg.SenderTag != "" {
		return
	}

	if msg.From != nil {
		a.adminsMux.Lock()
		chatAdmins := a.checkAdmins(ctx, b, msg.Chat.ID)
		a.adminsMux.Unlock()
		if _, isAdmin := chatAdmins[msg.From.ID]; isAdmin {
			return
		}
	}

	a.settingsMux.Lock()
	chatSettings := a.getChatSettings(ctx, msg.Chat.ID)
	a.settingsMux.Unlock()
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
		tgutil.MakePublicGroupString(msg.Chat.ID), msg.ID)
	text := fmt.Sprintf(
		"Подозрительная активность: [сообщение](%s) отредактировано с добавлением ссылки спустя %d с\n\n%s",
		msgLink, editDelaySec, tgutil.QuoteText(updatedText),
	)

	systemMessage(ctx, b, msg.Chat.ID, text, 5*60)
}

const likesTopN = 10

// renderLikesPage builds the text and nav keyboard for one page of the
// reaction leaderboard. page is 0-indexed.
func (a *App) renderLikesPage(ctx context.Context, chatID int64, page int) (text string, kb *models.InlineKeyboardMarkup) {
	entries := a.reactionCache.FilterSorted(func(k reactionKey) bool {
		return k.chatID == chatID
	})

	if len(entries) == 0 {
		return "Реакций пока нет", emptyInlineKeyboard()
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
		handle := tgutil.Escape(e.username)
		if handle == "" {
			handle = "—"
		}
		displayName := tgutil.Escape(e.altUsername)
		if displayName == "" {
			displayName = "—"
		}
		messageCount := "—"
		if user, err := storage.GetUser(ctx, e.userID); err == nil {
			messageCount = fmt.Sprintf("%d", user.Counter)
		}
		rows = append(rows, fmt.Sprintf("%s \\| %s \\| %s \\| %s \\| tg://user?id\\=%d", handle, displayName, e.emoji, messageCount, e.userID))
	}
	rows = append(rows, fmt.Sprintf("Страница %d/%d", page+1, pageCount))

	text = strings.Join(rows, "\n")
	kb = getLikesKeyboard(chatID, page, page > 0, page < pageCount-1)
	return text, kb
}

func (a *App) likesHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID

	deleteCommand(ctx, b, update.Message)

	text, kb := a.renderLikesPage(ctx, chatID, 0)

	// The leaderboard is shown only to the requester; sendSystem falls back to
	// a public message (private chat, anonymous sender, API refusal).
	sent, err := sendSystem(ctx, b, chatID, 0, &bot.SendMessageParams{
		Text:        text,
		ParseMode:   models.ParseModeMarkdown,
		ReplyMarkup: kb,
	}, resolveSendConfig([]sendOption{ephemeralFor(update.Message)}))
	if err != nil {
		zap.S().Infof("[likesHandler] SendMessage failed chatID=%d: %v", chatID, err)
		return
	}
	go delay(ctx, 5*60, func() {
		sent.delete(ctx, b)
	})
}

// detectorMiddleware feeds all message and edit updates into the detection goroutine.
func (a *App) detectorMiddleware(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		select {
		case a.detectorCh <- update:
		default:
			zap.S().Info("[detector] channel full, dropping update")
		}
		next(ctx, b, update)
	}
}
