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

func makeTextOnlyMessage(b *BanInfo) string {
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
	messageLink := fmt.Sprintf("[Ссылка на сообщение](tg://privatepost?channel=%s&post=%d)", makePublicGroupString(b.ChatID), b.TargetMessageID)

	return fmt.Sprintf("Голосование за режим только текст \\(без стикеров и картинок\\) %s\nДля решения необходим перевес в %d голосов\n%s\n%s", username, b.Score, messageLink, text)
}

func getTextOnlyInfoByUserID(ctx context.Context, chatID int64, userID int64) (*BanInfo, error) {
	banInfo := prepareBanInfo(ctx, chatID, userID)
	banInfo.Type = TEXT_ONLY
	banInfo.BanMessage = makeTextOnlyMessage(banInfo)
	return banInfo, nil
}

func getTextOnlyInfoByUser(ctx context.Context, chatID int64, username string) (banInfo *BanInfo, err error) {
	user, err := getRatingFromUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return getTextOnlyInfoByUserID(ctx, chatID, user.Userid)

}

func getTextOnlyInfo(ctx context.Context, chatID int64, messageID int64) (banInfo *BanInfo, err error) {
	banInfo = &BanInfo{
		ChatID:          chatID,
		TargetMessageID: messageID,
		Type:            TEXT_ONLY,
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
	banInfo.BanMessage = makeTextOnlyMessage(banInfo)
	return banInfo, nil

}

func textOnlyUser(ctx context.Context, b *bot.Bot, s *BanInfo) bool {
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
		UntilDate: getTextOnlyDuration(userRecord),
		Permissions: &models.ChatPermissions{
			CanSendMessages:      true,
			CanSendOtherMessages: false,
			CanSendPhotos:        false,
			CanSendVideos:        false,
			CanSendVideoNotes:    false,
			CanSendVoiceNotes:    false,
		},
		UseIndependentChatPermissions: false,
	})
	if err != nil {
		log.Printf("[textOnlyUser] RestrictChatMember failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
	}

	if result {
		err = userAddMuteCounter(ctx, s.UserID)
		if err != nil {
			log.Printf("[textOnlyUser] userAddMuteCounter failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
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

	resultText := fmt.Sprintf("Ограничение «только текст» выдано на %s", getTextOnlyDurationText(userRecord))
	if !result {
		resultText = "Не удалось выдать ограничение"
	}
	maker, err := getUser(ctx, s.OwnerID)
	ownerInfo := ""
	if err == nil {
		ownerInfo = fmt.Sprintf("Инициатор голосования: %s", maker.toClickableUsername())
	}

	chatName := escape(getChatNameFromSettings(s.ChatID))

	report := fmt.Sprintf("%s\n%s %s\n%s", chatName, resultText, banUsertag, ownerInfo)

	userMessages, err := getUserLastNthMessages(ctx, s.UserID, s.ChatID, 20)
	messageIDs := make([]int, len(userMessages))

	if err == nil && len(userMessages) > 0 {

		text := make([]string, 0, len(userMessages))
		for i, v := range userMessages {
			messageIDs[i] = int(v.MessageID)
			text = append(text, quoteText(v.Text))
		}
		escapedText := strings.Join(text, "\n")
		escapedText = firstN(escapedText, 3500)
		report = fmt.Sprintf("%s\nПоследние сообщения от пользователя:\n%s", report, escapedText)
	}
	// log.Println(report)

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
			log.Printf("[textOnlyUser] can't send report to recipientID=%d: %v", v, err)
		}
	}
	settingsMux.Unlock()

	if result {
		// do not notify if you failed
		params := &bot.SendMessageParams{
			ChatID:    s.ChatID,
			Text:      escape(fmt.Sprintf("Вам запрещена отправка картинок и стикеров на %s. Надеемся на понимание.", getTextOnlyDurationText(userRecord))),
			ParseMode: models.ParseModeMarkdown,
		}
		if s.TargetMessageID != 0 {
			params.ReplyParameters = &models.ReplyParameters{
				ChatID:    s.ChatID,
				MessageID: int(s.TargetMessageID),
			}
		}
		b.SendMessage(ctx, params)

	}

	return result
}

func getTextOnlyDurationInDays(user UserRecord) int {
	return 1 + int(math.Round(math.Log2(float64(user.MuteCounter+1))))
}
func getTextOnlyDuration(user UserRecord) int {
	currentTime := int(time.Now().Unix())
	return currentTime + 86400*(getTextOnlyDurationInDays(user))
}

func getTextOnlyDurationTextFromDays(muteDurationInDays int) (muteDuration string) {
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

func getTextOnlyDurationText(user UserRecord) string {
	textOnlyDurationInDays := getTextOnlyDurationInDays(user)
	return getTextOnlyDurationTextFromDays(textOnlyDurationInDays)
}
