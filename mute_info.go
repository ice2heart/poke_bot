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

	return fmt.Sprintf("Голосование за мут %s\nДля решения необходим перевес в %d голосов\n%s\n%s", username, b.Score, messageLink, text)
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
		banInfo.LastMessage = "Сообщение не найдено"
	} else {
		banInfo.LastMessage = messages[0].Text
		banInfo.TargetMessageID = messages[0].MessageID
	}
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

	settingsMux.Lock()
	chatSettings := getChatSettings(ctx, chatId)
	if chatSettings.Pause {
		settingsMux.Unlock()
		onPauseMessage(ctx, b, update.Message)
		return
	}
	settingsMux.Unlock()

	if len(update.Message.Entities) == 1 {
		systemAnswerToMessage(ctx, b, chatId, update.Message.ID, escape("Укажите ссылку на сообщение или пользователя.\nПримеры:\n/mute https://t.me/c/1657123097/2854347\n/mute @username"))
	}

	log.Printf("[muteHandler] new /mute from userID=%d in chatID=%d: %q", update.Message.From.ID, chatId, update.Message.Text)

	for _, v := range update.Message.Entities {
		log.Printf("[muteHandler] entity type=%v text=%q in chatID=%d", v.Type, update.Message.Text[v.Offset:v.Offset+v.Length], chatId)
		var err error
		var banInfo *BanInfo
		if v.Type == models.MessageEntityTypeTextMention {
			log.Printf("[muteHandler] text mention: userID=%d name=%q %q in chatID=%d", v.User.ID, v.User.FirstName, v.User.LastName, chatId)
			banInfo, err = getMuteInfoByUserID(ctx, chatId, v.User.ID)
			if err != nil {
				log.Printf("[muteHandler] getMuteInfoByUserID failed for userID=%d in chatID=%d: %v", v.User.ID, chatId, err)
				continue
			}
		}
		if v.Type == models.MessageEntityTypeMention {
			username := update.Message.Text[v.Offset+1 : v.Offset+v.Length]
			log.Printf("[muteHandler] processing mention @%s in chatID=%d", username, chatId)
			if username == myID {
				continue
			}
			banInfo, err = getMuteInfoByUser(ctx, chatId, username)
			if err != nil {
				log.Printf("[muteHandler] getMuteInfoByUser failed for username=%q in chatID=%d: %v", username, chatId, err)
				continue
			}
		}
		if v.Type == models.MessageEntityTypeURL {
			log.Printf("[muteHandler] processing URL entity in chatID=%d: text=%q hiddenURL=%q", chatId, update.Message.Text[v.Offset:v.Offset+v.Length], v.URL)

			chatLinks := parseChatLink(update.Message.Text[v.Offset:v.Offset+v.Length], chatId, update.Message.Chat.Username)

			for _, chatLink := range chatLinks {
				if chatLink.err != nil {
					log.Printf("[muteHandler] failed to parse chat link in chatID=%d: %v", chatId, chatLink.err)
					continue
				}
				banInfo, err = getMuteInfo(ctx, chatId, chatLink.TargetMessageID)
				if err != nil {
					systemAnswerToMessage(ctx, b, chatId, update.Message.ID, "Сообщение не найдено. Используйте альтернативный метод: /mute @username")
					continue
				}
				banInfo.TargetMessageID = chatLink.TargetMessageID
			}

		}
		if banInfo == nil {
			continue
		}

		banInfo.OwnerID = update.Message.From.ID
		// if used a chat alias should be saved proper ID
		if update.Message.SenderChat != nil {
			banInfo.OwnerID = update.Message.SenderChat.ID
		}
		banInfo.RequestMessageID = int64(update.Message.ID)
		if checkForDuplicates(ctx, chatId, banInfo.UserID, b, update) {
			continue
		}
		log.Printf("[muteHandler] starting mute vote: userID=%d chatID=%d requiredScore=%d", banInfo.UserID, chatId, banInfo.Score)
		if !makeVoteMessage(ctx, banInfo, b) {
			continue
		}
	}

}

func muteUser(ctx context.Context, b *bot.Bot, s *BanInfo) {
	user, err := getUser(ctx, s.UserID)
	var userRecord UserRecord
	var banUsertag string

	if err == nil {
		userRecord = *user
		banUsertag = user.toClickableUsername()
	} else {
		banUsertag = fmt.Sprintf("[Пользователь вне базы](tg://user?id=%d)", s.UserID)
	}

	result, err := b.RestrictChatMember(ctx, &bot.RestrictChatMemberParams{
		ChatID:    s.ChatID,
		UserID:    s.UserID,
		UntilDate: getMuteDuration(userRecord),
		Permissions: &models.ChatPermissions{
			CanSendOtherMessages:  false,
			CanAddWebPagePreviews: false,
			CanSendPolls:          false,
		},
		UseIndependentChatPermissions: false,
	})
	if err != nil {
		log.Printf("[muteUser] RestrictChatMember failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
	}

	if result {
		err = userAddMuteCounter(ctx, s.UserID)
		if err != nil {
			log.Printf("[muteUser] userAddMuteCounter failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
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

	resultText := fmt.Sprintf("Мут выдан на %s", getMuteDurationText(userRecord))
	if !result {
		resultText = "Не удалось выдать мут"
	}
	maker, err := getUser(ctx, s.OwnerID)
	ownerInfo := ""
	if err == nil {
		ownerInfo = fmt.Sprintf("Инициатор голосования: %s", maker.toClickableUsername())
	}

	chatName := getChatNameFromSettings(s.ChatID)

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
	report = strings.ReplaceAll(report, "-", "\\-")

	pushBanLog(ctx, s)
	disablePreview := &models.LinkPreviewOptions{IsDisabled: bot.True()}

	settingsMux.Lock()
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
			log.Printf("[muteUser] can't send report to recipientID=%d: %v", v, err)
		}
	}
	settingsMux.Unlock()

	if result {
		// do not notify if you failed
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    s.ChatID,
			Text:      fmt.Sprintf("Вам выдан мут на %s. Надеемся на понимание.", getMuteDurationText(userRecord)),
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

func getMuteDurationTextFromDays(muteDurationInDays int) (muteDuration string) {
	if muteDurationInDays == 1 {
		muteDuration = "сутки"
	} else if muteDurationInDays%10 == 1 && muteDurationInDays >= 20 {
		muteDuration = fmt.Sprintf("%d день", muteDurationInDays)
	} else if (muteDurationInDays > 20 && muteDurationInDays%10 >= 5) || ((muteDurationInDays >= 5) && (muteDurationInDays <= 20)) || (muteDurationInDays > 20 && muteDurationInDays%10 == 0) {
		muteDuration = fmt.Sprintf("%d дней", muteDurationInDays)
	} else if muteDurationInDays < 5 || (muteDurationInDays > 20 && muteDurationInDays%10 < 5) {
		muteDuration = fmt.Sprintf("%d дня", muteDurationInDays)
	}
	return
}

func getMuteDurationText(user UserRecord) string {
	muteDurationInDays := getMuteDurationInDays(user)
	return getMuteDurationTextFromDays(muteDurationInDays)
}
