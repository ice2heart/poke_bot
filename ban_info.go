package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

const (
	BAN uint8 = iota
	MUTE
	TEXT_ONLY
)

type BanInfo struct {
	ChatID           int64
	TargetMessageID  int64
	UserID           int64
	Score            int16
	OwnerID          int64
	RequestMessageID int64
	VoteMessageID    int64
	UserName         string
	ProfileName      string
	LastMessage      string
	BanMessage       string
	Voters           map[int64]int8
	Type             uint8
	CreatedAt        time.Time
	cancelPin        context.CancelFunc
}

const (
	DEFAULT_SCORE int16 = 3
	LOW_SCORE     int16 = 3
	MID_SCORE     int16 = 5
	HIGH_SCORE    int16 = 10
)

func makeBanMessage(b *BanInfo) string {
	text := b.LastMessage
	if len(text) > 200 {
		text = firstN(text, 200)
	}
	text = quoteText(text)

	var username string
	if b.UserName == "" {
		username = fmt.Sprintf("[%s](tg://user?id=%d)", strings.TrimSpace(escape(b.ProfileName)), b.UserID)
	} else {
		username = fmt.Sprintf("@%s", escape(b.UserName))
	}
	messageLink := ""
	if b.TargetMessageID != 0 {
		messageLink = fmt.Sprintf("[Ссылка на сообщение](tg://privatepost?channel=%s&post=%d)", makePublicGroupString(b.ChatID), b.TargetMessageID)
	}

	return fmt.Sprintf("Голосование за блокировку %s\nДля решения необходим перевес в %d голосов\n%s\n%s", username, b.Score, messageLink, text)
}

func calculateRequiredRating(userScore uint32) (requiredScore int16) {
	if userScore < 10 {
		requiredScore = LOW_SCORE
	} else if userScore < 100 {
		requiredScore = MID_SCORE
	} else {
		requiredScore = HIGH_SCORE
	}
	return requiredScore
}

func getBanInfo(ctx context.Context, chatID int64, messageID int64) (banInfo *BanInfo, err error) {
	banInfo = &BanInfo{
		ChatID:          chatID,
		TargetMessageID: messageID,
		Type:            BAN,
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
	banInfo.BanMessage = makeBanMessage(banInfo)
	return banInfo, nil
}

func getBanInfoByUsername(ctx context.Context, chatID int64, username string) (*BanInfo, error) {
	user, err := getRatingFromUsername(ctx, username)
	if err == nil {
		return getBanInfoByUserID(ctx, chatID, user.Userid)
	}
	userInfo, mtErr := client.GetUserByUsername(ctx, username)
	if mtErr != nil {
		return nil, err
	}
	return newBanInfoNoDB(chatID, userInfo.UserId, userInfo.Username, BAN, makeBanMessage), nil
}

func getBanInfoByUserID(ctx context.Context, chatID int64, userID int64) (*BanInfo, error) {
	banInfo := prepareBanInfo(ctx, chatID, userID)
	banInfo.Type = BAN
	banInfo.BanMessage = makeBanMessage(banInfo)
	return banInfo, nil
}

func banUser(ctx context.Context, b *bot.Bot, s *BanInfo) bool {

	// jcart, _ := json.MarshalIndent(s, "", "\t")
	// fmt.Println(string(jcart))

	var banUsertag string

	cacheBanInfo(s.ChatID, s.UserID)
	reactionCache.Delete(reactionKey{chatID: s.ChatID, userID: s.UserID})

	if len(s.UserName) != 0 {
		banUsertag = fmt.Sprintf("@%s", escape(s.UserName))
	} else {
		helperUser, err := getUser(ctx, s.UserID)
		if err != nil {
			banUsertag = fmt.Sprintf("[Пользователь вне базы](tg://user?id=%d)", s.UserID)
		} else {
			banUsertag = helperUser.toClickableUsername()
		}
	}

	result, err := b.BanChatMember(ctx, &bot.BanChatMemberParams{
		ChatID: s.ChatID,
		UserID: s.UserID,
	})
	if err != nil {
		zap.S().Infof("[banUser] BanChatMember failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
	}

	if !result && len(s.UserName) != 0 {
		// Use the MTproto client to try ban
		settingsMux.Lock()
		settings := getChatSettings(ctx, s.ChatID)
		result, err = client.BanUser(ctx, settings.ChatID, settings.ChatAccessHash, s.UserName)
		settingsMux.Unlock()
		if err != nil {
			zap.S().Infof("[banUser] MTProto fallback ban failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
		}
	}
	resultText := "Заблокирован успешно"
	if !result {
		resultText = "Не удалось заблокировать"
	}
	maker, err := getUser(ctx, s.OwnerID)
	ownerInfo := ""
	if err == nil {
		ownerInfo = fmt.Sprintf("Инициатор голосования: %s", maker.toClickableUsername())
	}

	chatName := escape(getChatNameFromSettings(s.ChatID))

	report := fmt.Sprintf("%s\n%s %s\n%s", chatName, resultText, banUsertag, ownerInfo)

	userMessages, err := getUserLastDaysMessages(ctx, s.UserID, s.ChatID, 2)

	// Collect every message ID to delete, de-duplicated: the vote/request/target
	// messages plus the user's recent messages.
	toDelete := map[int64]struct{}{
		s.TargetMessageID:  {},
		s.VoteMessageID:    {},
		s.RequestMessageID: {},
	}
	if err == nil && len(userMessages) > 0 {
		text := make([]string, 0, len(userMessages))
		for _, v := range userMessages {
			toDelete[v.MessageID] = struct{}{}
			text = append(text, quoteText(v.Text))
		}
		escapedText := firstN(strings.Join(text, "\n"), 3500)
		report = fmt.Sprintf("%s\nПоследние сообщения от пользователя:\n%s", report, escapedText)
	}
	delete(toDelete, 0) // never try to delete message ID 0

	// Deletes are independent round-trips; fire them concurrently.
	var delWg sync.WaitGroup
	for id := range toDelete {
		delWg.Add(1)
		go func(id int64) {
			defer delWg.Done()
			if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{
				ChatID:    s.ChatID,
				MessageID: int(id),
			}); err != nil {
				zap.S().Infof("[banUser] can't delete messageID=%d for userID=%d in chatID=%d: %v", id, s.UserID, s.ChatID, err)
			}
		}(id)
	}
	delWg.Wait()

	pushBanLog(ctx, s)
	disablePreview := &models.LinkPreviewOptions{IsDisabled: bot.True()}

	settingsMux.Lock()
	defer settingsMux.Unlock()

	chatSettings := getChatSettings(ctx, s.ChatID)

	keyboard := getBanMessageKeyboard(s.ChatID, s.UserID, s.VoteMessageID)
	var sendWg sync.WaitGroup
	for _, v := range chatSettings.LogRecipients {
		sendWg.Add(1)
		go func(recipientID int64) {
			defer sendWg.Done()
			if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:             recipientID,
				Text:               report,
				ParseMode:          models.ParseModeMarkdown,
				ReplyMarkup:        keyboard,
				LinkPreviewOptions: disablePreview,
			}); err != nil {
				zap.S().Infof("[banUser] can't send report to recipientID=%d: %v", recipientID, err)
			}
		}(v)
	}
	sendWg.Wait()

	if result {
		reactionCache.Delete(reactionKey{chatID: s.ChatID, userID: s.UserID})
	}
	return result
}
