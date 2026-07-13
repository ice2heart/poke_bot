package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

// makeVoteText builds the MarkdownV2 body of a vote message. action is the
// verb-phrase describing what is being voted on (e.g. "блокировку", "мут").
func makeVoteText(b *BanInfo, action string) string {
	text := quoteText(firstN(b.LastMessage, 200))
	username := userTag(b.UserName, b.ProfileName, b.UserID)
	messageLink := ""
	if b.TargetMessageID != 0 {
		messageLink = fmt.Sprintf("[Ссылка на сообщение](tg://privatepost?channel=%s&post=%d)",
			makePublicGroupString(b.ChatID), b.TargetMessageID)
	}
	return fmt.Sprintf("Голосование за %s %s\nДля решения необходим перевес в %d голосов\n%s\n%s",
		action, username, b.Score, messageLink, text)
}

// getInfoByUserID builds a BanInfo for a known user, tagged with the given type
// and rendered with makeMessage. Used by the ban/mute/text_only variants.
func getInfoByUserID(ctx context.Context, chatID, userID int64, banType uint8, makeMessage func(*BanInfo) string) (*BanInfo, error) {
	banInfo := prepareBanInfo(ctx, chatID, userID)
	banInfo.Type = banType
	banInfo.BanMessage = makeMessage(banInfo)
	return banInfo, nil
}

// getInfoByMessage builds a BanInfo from a stored chat message, tagged with the
// given type and rendered with makeMessage.
func getInfoByMessage(ctx context.Context, chatID, messageID int64, banType uint8, makeMessage func(*BanInfo) string) (*BanInfo, error) {
	banInfo := &BanInfo{
		ChatID:          chatID,
		TargetMessageID: messageID,
		Type:            banType,
	}
	chatMessage, err := getMessageInfo(ctx, chatID, messageID)
	if err != nil {
		return nil, err
	}
	banInfo.UserName = chatMessage.UserName
	banInfo.LastMessage = chatMessage.Text
	banInfo.UserID = chatMessage.UserID
	user, err := getUser(ctx, banInfo.UserID)
	if err != nil {
		return nil, err
	}
	if user.AltUsername != "" {
		banInfo.ProfileName = user.AltUsername
	}
	banInfo.Score = calculateRequiredRating(user.Counter)
	banInfo.BanMessage = makeMessage(banInfo)
	return banInfo, nil
}

// deleteMessagesConcurrently deletes every given message ID in parallel,
// skipping zero IDs and de-duplicating, and returns once all deletes finish.
// tag is used as the log prefix on failures.
func deleteMessagesConcurrently(ctx context.Context, b *bot.Bot, chatID int64, ids []int64, tag string) {
	seen := make(map[int64]struct{}, len(ids))
	var wg sync.WaitGroup
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{
				ChatID:    chatID,
				MessageID: int(id),
			}); err != nil {
				zap.S().Infof("[%s] can't delete messageID=%d in chatID=%d: %v", tag, id, chatID, err)
			}
		}(id)
	}
	wg.Wait()
}

// appendRecentMessages appends the given user's recent messages to report as a
// quoted, length-capped block, and returns their message IDs. It never fails:
// on error or no messages the report and an empty slice are returned unchanged.
func appendRecentMessages(report string, userMessages []ChatMessage) (string, []int64) {
	if len(userMessages) == 0 {
		return report, nil
	}
	ids := make([]int64, 0, len(userMessages))
	text := make([]string, 0, len(userMessages))
	for _, v := range userMessages {
		ids = append(ids, v.MessageID)
		text = append(text, quoteText(v.Text))
	}
	escapedText := firstN(strings.Join(text, "\n"), 3500)
	report = fmt.Sprintf("%s\nПоследние сообщения от пользователя:\n%s", report, escapedText)
	return report, ids
}

// sendReportToRecipients sends the report to every log recipient concurrently,
// under settingsMux, and returns once all sends finish. keyboard is the inline
// keyboard attached to each report. tag is the log prefix on failures.
func sendReportToRecipients(ctx context.Context, b *bot.Bot, chatID int64, report string, keyboard models.ReplyMarkup, tag string) {
	disablePreview := &models.LinkPreviewOptions{IsDisabled: bot.True()}

	settingsMux.Lock()
	defer settingsMux.Unlock()
	chatSettings := getChatSettings(ctx, chatID)

	var wg sync.WaitGroup
	for _, v := range chatSettings.LogRecipients {
		wg.Add(1)
		go func(recipientID int64) {
			defer wg.Done()
			if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:             recipientID,
				Text:               report,
				ParseMode:          models.ParseModeMarkdown,
				ReplyMarkup:        keyboard,
				LinkPreviewOptions: disablePreview,
			}); err != nil {
				zap.S().Infof("[%s] can't send report to recipientID=%d: %v", tag, recipientID, err)
			}
		}(v)
	}
	wg.Wait()
}

// buildModerationReport builds the standard "<chat>\n<result> <user>\n<owner>"
// report header for a completed moderation action.
func buildModerationReport(ctx context.Context, chatID, ownerID int64, resultText, banUsertag string) string {
	ownerInfo := ""
	if maker, err := getUser(ctx, ownerID); err == nil {
		ownerInfo = fmt.Sprintf("Инициатор голосования: %s", maker.toClickableUsername())
	}
	chatName := escape(getChatNameFromSettings(chatID))
	return fmt.Sprintf("%s\n%s %s\n%s", chatName, resultText, banUsertag, ownerInfo)
}

// restrictionDurationInDays is the shared escalating duration used by mute and
// text-only restrictions: 1 day, growing with log2 of the user's mute counter.
func restrictionDurationInDays(user UserRecord) int {
	return 1 + int(math.Round(math.Log2(float64(user.MuteCounter+1))))
}

// restrictionUntilDate converts the escalating duration to a Telegram UntilDate.
func restrictionUntilDate(user UserRecord) int {
	return int(time.Now().Unix()) + 86400*restrictionDurationInDays(user)
}

// restrictionDurationText renders the escalating duration as a Russian
// pluralized day count (e.g. "сутки", "3 дня", "7 дней").
func restrictionDurationText(user UserRecord) string {
	return restrictionDurationTextFromDays(restrictionDurationInDays(user))
}

func restrictionDurationTextFromDays(days int) (out string) {
	switch {
	case days == 1:
		out = "сутки"
	case days%10 == 1 && days >= 20:
		out = fmt.Sprintf("%d день", days)
	case (days > 20 && days%10 >= 5) || (days >= 5 && days <= 20) || (days > 20 && days%10 == 0):
		out = fmt.Sprintf("%d дней", days)
	case days < 5 || (days > 20 && days%10 < 5):
		out = fmt.Sprintf("%d дня", days)
	}
	return
}
