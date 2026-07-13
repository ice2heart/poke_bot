package main

import (
	"context"
	"fmt"
	"time"

	"github.com/go-telegram/bot"
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
	return makeVoteText(b, "блокировку")
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

func getBanInfo(ctx context.Context, chatID int64, messageID int64) (*BanInfo, error) {
	return getInfoByMessage(ctx, chatID, messageID, BAN, makeBanMessage)
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
	return getInfoByUserID(ctx, chatID, userID, BAN, makeBanMessage)
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
	report := buildModerationReport(ctx, s.ChatID, s.OwnerID, resultText, banUsertag)

	userMessages, _ := getUserLastDaysMessages(ctx, s.UserID, s.ChatID, 2)
	report, recentIDs := appendRecentMessages(report, userMessages)

	// Delete the vote/request/target messages plus the user's recent messages.
	deleteIDs := append([]int64{s.TargetMessageID, s.VoteMessageID, s.RequestMessageID}, recentIDs...)
	deleteMessagesConcurrently(ctx, b, s.ChatID, deleteIDs, "banUser")

	pushBanLog(ctx, s)
	sendReportToRecipients(ctx, b, s.ChatID, report,
		getBanMessageKeyboard(s.ChatID, s.UserID, s.VoteMessageID), "banUser")

	if result {
		reactionCache.Delete(reactionKey{chatID: s.ChatID, userID: s.UserID})
	}
	return result
}
