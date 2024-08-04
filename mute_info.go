package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func makeMuteMessage(b *BanInfo) string {
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
	messageLink := fmt.Sprintf("[Ссылка на сообщение](tg://privatepost?channel=%s&post=%d)", makePublicGroupString(b.ChatID), b.TargetMessageID)

	return fmt.Sprintf("Голосуем за мут %s \nНеобходим перевес в %d голосов\n%s\n%s", username, b.Score, messageLink, text)
}

func getMuteInfoByUserID(ctx context.Context, chatID int64, userID int64) (banInfo *BanInfo, err error) {
	banInfo = &BanInfo{
		ChatID: chatID,
		UserID: userID,
		Type:   MUTE,
	}

	user, err := getUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	banInfo.ProfileName = user.AltUsername
	banInfo.UserName = user.Username
	banInfo.Score = calculateRequiredRating(user.Counter)

	messages, err := getUserLastNthMessages(ctx, userID, chatID, 1)
	if err != nil || len(messages) == 0 {
		banInfo.LastMessage = "Not found"
	}
	banInfo.LastMessage = messages[0].Text
	banInfo.TargetMessageID = messages[0].MessageID
	banInfo.BanMessage = makeMuteMessage(banInfo)
	return banInfo, nil

}

func getMuteInfoByUser(ctx context.Context, chatID int64, username string) (banInfo *BanInfo, err error) {
	user, err := getRatingFromUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return getMuteInfoByUserID(ctx, chatID, user.Userid)

}

func getMuteInfo(ctx context.Context, chatID int64, messageID int64) (banInfo *BanInfo, err error) {
	banInfo = &BanInfo{
		ChatID:          chatID,
		TargetMessageID: messageID,
		Type:            MUTE,
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
	banInfo.BanMessage = makeMuteMessage(banInfo)
	return banInfo, nil

}

func muteHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatId := update.Message.Chat.ID
	chatSettings := getChatSettings(ctx, chatId)
	if chatSettings.Pause {
		onPauseMessage(ctx, b, update.Message)
		return
	}

	if len(update.Message.Entities) == 1 {
		systemAnswerToMessage(ctx, b, chatId, update.Message.ID, escape("Для использования бота необходимо указать ему ссылку на сообщение или указать пользователя\nНапример:\n/mute https://t.me/c/1657123097/2854347\n/mute @username"))
	}

	log.Printf("MUTE: New message: %s", update.Message.Text)

	for _, v := range update.Message.Entities {
		log.Printf("MUTE: Message entities type: %v, text %s", v.Type, update.Message.Text[v.Offset:v.Offset+v.Length])
		var err error
		var banInfo *BanInfo
		if v.Type == models.MessageEntityTypeTextMention {
			log.Printf("MUTE: user mentioned type: %v , userID: %v, Alt Name %v %v", v.Type, v.User.ID, v.User.FirstName, v.User.LastName)
			banInfo, err = getMuteInfoByUserID(ctx, chatId, v.User.ID)
			if err != nil {
				log.Printf("TODO: return error to user! %v", err)
				continue
			}
		}
		if v.Type == models.MessageEntityTypeMention {
			username := update.Message.Text[v.Offset+1 : v.Offset+v.Length]
			log.Printf("MUTE: user mentioned username @%s", username)
			if username == myID {
				continue
			}
			banInfo, err = getMuteInfoByUser(ctx, chatId, username)
			if err != nil {
				log.Printf("TODO: return error to user! %v", err)
				continue
			}
		}
		if v.Type == models.MessageEntityTypeURL {
			log.Printf("MUTE: the message URL. The hidden url:%s, text: %s", v.URL, update.Message.Text[v.Offset:v.Offset+v.Length])

			chatLinks := parseChatLink(update.Message.Text[v.Offset:v.Offset+v.Length], chatId, update.Message.Chat.Username)

			for _, chatLink := range chatLinks {
				if chatLink.err != nil {
					log.Printf("MUTE: parsing link was failed: %v", chatLink.err)
					continue
				}
				banInfo, err = getMuteInfo(ctx, chatId, chatLink.TargetMessageID)
				if err != nil {
					systemAnswerToMessage(ctx, b, chatId, update.Message.ID, "Извините сообщение не найдено, исользуйте альтернативный метод через \"/mute @username\"")
					continue
				}
				banInfo.TargetMessageID = chatLink.TargetMessageID
			}

		}
		if banInfo == nil {
			continue
		}

		banInfo.OwnerID = update.Message.From.ID
		banInfo.RequestMessageID = int64(update.Message.ID)
		if checkForDuplicates(ctx, chatId, banInfo.UserID, b, update) {
			continue
		}
		log.Printf("MUTE: Start vote process score for user %d score: %d", banInfo.UserID, banInfo.Score)
		if !makeVoteMessage(ctx, banInfo, b) {
			continue
		}
	}

}

func muteUser(ctx context.Context, b *bot.Bot, s *BanInfo) {
	user, err := getUser(ctx, s.UserID)
	var banUsertag string

	if err == nil {
		banUsertag = user.toClickableUsername()
	} else {
		banUsertag = fmt.Sprintf("[Пользователь вне базы](tg://user?id=%d)", s.UserID)
	}

	result, err := b.RestrictChatMember(ctx, &bot.RestrictChatMemberParams{
		ChatID:    s.ChatID,
		UserID:    s.UserID,
		UntilDate: getMuteDuration(*user),
		Permissions: &models.ChatPermissions{
			CanSendOtherMessages:  false,
			CanAddWebPagePreviews: false,
			CanSendPolls:          false,
		},
		UseIndependentChatPermissions: false,
	})
	if err != nil {
		log.Printf("Can't mute user %v %d ", err, s.ChatID)
	}

	if result {
		err = userAddMuteCounter(ctx, s.UserID)
		if err != nil {
			log.Printf("Can't add mute counter %v %d ", err, s.ChatID)
		}
	}

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

	resultText := fmt.Sprintf("Успешно замьючен на %s", getMuteDurationText(*user))
	if !result {
		resultText = "Не смог замьютить"
	}
	maker, err := getUser(ctx, s.OwnerID)
	ownerInfo := ""
	if err == nil {
		ownerInfo = fmt.Sprintf("Автор голосовалки %s", maker.toClickableUsername())
	}
	report := fmt.Sprintf("%s %s\n%s", resultText, banUsertag, ownerInfo)

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
		report = fmt.Sprintf("%s\nПоследние сообщения от пользователя:\n%s", report, escapedText)
	}
	// log.Println(report)

	pushBanLog(ctx, s)
	disablePreview := &models.LinkPreviewOptions{IsDisabled: bot.True()}

	chatSettings := getChatSettings(ctx, s.ChatID)

	for _, v := range chatSettings.LogRecipients {
		_, err = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:             v,
			Text:               report,
			ParseMode:          models.ParseModeMarkdown,
			ReplyMarkup:        getMuteMessageKeyboard(s.ChatID, s.UserID),
			LinkPreviewOptions: disablePreview,
		})
		if err != nil {
			log.Printf("Can't send report %v", err)
		}
	}

	if result {
		// do not notify if you failed
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    s.ChatID,
			Text:      fmt.Sprintf("Вам выдан мут на %s, надеемся на ваше понимание", getMuteDurationText(*user)),
			ParseMode: models.ParseModeMarkdown,
			ReplyParameters: &models.ReplyParameters{
				ChatID:    s.ChatID,
				MessageID: int(s.TargetMessageID),
			},
		})
	}

}

func getMuteDurationInDays(user UserRecord) int {
	return 1 + int(math.Round(math.Log2(float64(user.MuteCounter+1))))
}
func getMuteDuration(user UserRecord) int {
	currentTime := int(time.Now().Unix())
	return currentTime + 86400*(getMuteDurationInDays(user))
}

func getMuteDurationTextFromDays(muteDuratuionInDays int) (muteDuratuion string) {
	if muteDuratuionInDays == 1 {
		muteDuratuion = "сутки"
	} else if muteDuratuionInDays%10 == 1 && muteDuratuionInDays >= 20 {
		muteDuratuion = fmt.Sprintf("%d день", muteDuratuionInDays)
	} else if (muteDuratuionInDays > 20 && muteDuratuionInDays%10 >= 5) || ((muteDuratuionInDays >= 5) && (muteDuratuionInDays <= 20)) || (muteDuratuionInDays > 20 && muteDuratuionInDays%10 == 0) {
		muteDuratuion = fmt.Sprintf("%d дней", muteDuratuionInDays)
	} else if muteDuratuionInDays < 5 || (muteDuratuionInDays > 20 && muteDuratuionInDays%10 < 5) {
		muteDuratuion = fmt.Sprintf("%d дня", muteDuratuionInDays)
	}
	return
}

func getMuteDurationText(user UserRecord) string {
	muteDuratuionInDays := getMuteDurationInDays(user)
	return getMuteDurationTextFromDays(muteDuratuionInDays)
}
