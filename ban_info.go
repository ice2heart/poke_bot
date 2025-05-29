package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
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
	Voiters          map[int64]int8
	Type             uint8
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
	lines := strings.Split(text, "\n")
	newLines := make([]string, 0, len(lines))
	for _, line := range lines {
		line = fmt.Sprintf(">%s", escape(line))
		newLines = append(newLines, line)
	}
	text = strings.Join(newLines, "\n")

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

func getBanInfoByUsername(ctx context.Context, chatID int64, username string) (banInfo *BanInfo, err error) {
	user, err := getRatingFromUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return getBanInfoByUserID(ctx, chatID, user.Userid)
}

func getBanInfoByUserIDNoDB(chatID int64, userID int64, username string) (banInfo *BanInfo) {
	banInfo = &BanInfo{
		ChatID:          chatID,
		UserID:          userID,
		UserName:        username,
		Score:           LOW_SCORE,
		LastMessage:     "Not found",
		TargetMessageID: 0,
		Type:            BAN,
	}
	banInfo.BanMessage = makeBanMessage(banInfo)
	return banInfo
}

func getBanInfoByUserID(ctx context.Context, chatID int64, userID int64) (banInfo *BanInfo, err error) {
	banInfo = &BanInfo{
		ChatID: chatID,
		UserID: userID,
		Type:   BAN,
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
		banInfo.LastMessage = "Not found"
	} else {
		banInfo.LastMessage = messages[0].Text
		banInfo.TargetMessageID = messages[0].MessageID
	}
	banInfo.BanMessage = makeBanMessage(banInfo)
	return banInfo, nil
}

func banUser(ctx context.Context, b *bot.Bot, s *BanInfo) {

	// jcart, _ := json.MarshalIndent(s, "", "\t")
	// fmt.Println(string(jcart))

	var banUsertag string

	if len(s.UserName) != 0 {
		banUsertag = fmt.Sprintf("@%s", escape(s.UserName))
	} else {
		helper_user, err := getUser(ctx, s.UserID)
		if err != nil {
			banUsertag = fmt.Sprintf("[Пользователь вне базы](tg://user?id=%d)", s.UserID)
		} else {
			banUsertag = helper_user.toClickableUsername()
		}
	}

	result, err := b.BanChatMember(ctx, &bot.BanChatMemberParams{
		ChatID: s.ChatID,
		UserID: s.UserID,
	})
	if err != nil {
		log.Printf("Can't ban user %v ", err)
		jcart, _ := json.MarshalIndent(s, "", "\t")
		log.Println(string(jcart))
	}

	if !result && len(s.UserName) != 0 {
		// Use the MTproto client to try ban
		settings := getChatSettings(ctx, s.ChatID)
		result, err = client.BanUser(ctx, settings.ChatID, settings.ChatAccessHash, s.UserName)
		if err != nil {
			log.Printf("MTProto ban is failed: %v", err)
		}
	}
	//Delete the target
	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    s.ChatID,
		MessageID: int(s.TargetMessageID),
	})
	//Delete the vote message
	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    s.ChatID,
		MessageID: int(s.VoteMessageID),
	})
	// Delete the vote request
	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    s.ChatID,
		MessageID: int(s.RequestMessageID),
	})

	resultText := "Успешно забанен"
	if !result {
		resultText = "Не смог забанить"
	}
	maker, err := getUser(ctx, s.OwnerID)
	ownerInfo := ""
	if err == nil {
		ownerInfo = fmt.Sprintf("Автор голосовалки %s", maker.toClickableUsername())
	}
	chatNameSettongs, ok := settings[s.ChatID]
	chatName := ""
	if ok {
		chatName = chatNameSettongs.ChatName
	}
	report := fmt.Sprintf("%s\n%s %s\n%s", chatName, resultText, banUsertag, ownerInfo)

	userMessages, err := getUserLastNthMessages(ctx, s.UserID, s.ChatID, 20)
	messageIDs := make([]int, len(userMessages))

	if err == nil && len(userMessages) > 0 {

		text := make([]string, 0, len(userMessages))
		for i, v := range userMessages {
			messageIDs[i] = int(v.MessageID)

			lines := strings.Split(v.Text, "\n")
			for _, line := range lines {
				line = fmt.Sprintf(">%s", escape(line))
				text = append(text, line)
			}
		}
		escapedText := strings.Join(text, "\n")
		escapedText = firstN(escapedText, 3500)
		report = fmt.Sprintf("%s\nПоследние сообщения от пользователя:\n%s", report, escapedText)
	}
	// log.Println(report)
	for _, v := range messageIDs {
		_, err = b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    s.ChatID,
			MessageID: int(v),
		})
		if err != nil {
			log.Printf("Can't delete messages for chat %d, user %d. IDs: %v. Err: %v", s.ChatID, s.UserID, v, err)
		}
	}
	// this is works like a shit
	// _, err = b.DeleteMessages(ctx, &bot.DeleteMessagesParams{
	// 	ChatID:     s.ChatID,
	// 	MessageIDs: messageIDs,
	// })
	// if err != nil {
	// 	log.Printf("Can't delete messages for chat %d, user %d. IDs: %v. Err: %v", s.ChatID, s.UserID, messageIDs, err)
	// }
	pushBanLog(ctx, s)
	disablePreview := &models.LinkPreviewOptions{IsDisabled: bot.True()}

	chatSettings := getChatSettings(ctx, s.ChatID)

	for _, v := range chatSettings.LogRecipients {
		_, err = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:             v,
			Text:               report,
			ParseMode:          models.ParseModeMarkdown,
			ReplyMarkup:        getBanMessageKeyboard(s.ChatID, s.UserID),
			LinkPreviewOptions: disablePreview,
		})
		if err != nil {
			log.Printf("Can't send report %v", err)
		}
	}
}
