package main

import (
	"context"
	"fmt"
	"strings"
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
	LatMessage       string
	BanMessage       string
	Voiters          map[int64]int8
}

const (
	DEFAULT_SCORE int16 = 3
	LOW_SCORE     int16 = 3
	MID_SCORE     int16 = 5
	HIGH_SCORE    int16 = 10
)

func makeBanMessage(b *BanInfo) string {
	text := b.LatMessage
	if len(text) > 200 {
		text = firstN(text, 200)
	}
	lines := strings.Split(text, "\n")
	newLines := make([]string, 0, len(lines))
	for _, line := range lines {
		line = fmt.Sprintf(">%s", escape(line))
		newLines = append(newLines, line)
	}
	text = strings.Join(newLines, "\n")

	var username string
	if b.UserName == "" {
		username = fmt.Sprintf("[%s](tg://user?id=%d)", escape(b.ProfileName), b.UserID)
	} else {
		username = fmt.Sprintf("@%s", escape(b.UserName))
	}
	messageLink := fmt.Sprintf("[Ссылка на сообщение](tg://privatepost?channel=%s&post=%d)", makePublicGroupString(b.ChatID), b.TargetMessageID)

	return fmt.Sprintf("Голосуем за бан %s \nНеобходим перевес в %d голосов\n%s\n%s", username, b.Score, messageLink, text)
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
	}
	chatMessage, err := getMessageInfo(ctx, chatID, messageID)
	if err != nil {
		return nil, err
	}
	banInfo.UserName = chatMessage.UserName
	banInfo.LatMessage = chatMessage.Text
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

func getBanInfoByUsername(ctx context.Context, chatID int64, username string) (banInfo *BanInfo, err error) {
	user, err := getRatingFromUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return getBanInfoByUserID(ctx, chatID, user.Userid)
}

func getBanInfoByUserID(ctx context.Context, chatID int64, userID int64) (banInfo *BanInfo, err error) {
	banInfo = &BanInfo{
		ChatID: chatID,
		UserID: userID,
	}
	user, err := getUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	banInfo.ProfileName = user.AltUsername
	banInfo.UserName = user.Username
	banInfo.Score = calculateRequiredRating(user.Counter)

	messages, err := getUserLastNthMessages(ctx, userID, chatID, 1)
	if err != nil {
		// MAYBE CAN BE USED as is
		return nil, err
	}
	if len(messages) == 0 {
		banInfo.LatMessage = "Not found"
	}
	banInfo.LatMessage = messages[0].Text
	banInfo.TargetMessageID = messages[0].MessageID
	banInfo.BanMessage = makeBanMessage(banInfo)
	return banInfo, nil
}
