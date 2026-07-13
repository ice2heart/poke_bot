package main

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

func makeTextOnlyMessage(b *BanInfo) string {
	return makeVoteText(b, "режим только текст \\(без стикеров и картинок\\)")
}

func getTextOnlyInfoByUserID(ctx context.Context, chatID int64, userID int64) (*BanInfo, error) {
	return getInfoByUserID(ctx, chatID, userID, TEXT_ONLY, makeTextOnlyMessage)
}

func getTextOnlyInfoByUser(ctx context.Context, chatID int64, username string) (*BanInfo, error) {
	user, err := getRatingFromUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return getTextOnlyInfoByUserID(ctx, chatID, user.Userid)
}

func getTextOnlyInfo(ctx context.Context, chatID int64, messageID int64) (*BanInfo, error) {
	return getInfoByMessage(ctx, chatID, messageID, TEXT_ONLY, makeTextOnlyMessage)
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
		UntilDate: restrictionUntilDate(userRecord),
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
		zap.S().Infof("[textOnlyUser] RestrictChatMember failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
	}

	if result {
		err = userAddMuteCounter(ctx, s.UserID)
		if err != nil {
			zap.S().Infof("[textOnlyUser] userAddMuteCounter failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
		}
	}

	deleteMessagesConcurrently(ctx, b, s.ChatID, []int64{s.VoteMessageID, s.RequestMessageID}, "textOnlyUser")

	resultText := fmt.Sprintf("Ограничение «только текст» выдано на %s", restrictionDurationText(userRecord))
	if !result {
		resultText = "Не удалось выдать ограничение"
	}
	report := buildModerationReport(ctx, s.ChatID, s.OwnerID, resultText, banUsertag)

	userMessages, _ := getUserLastNthMessages(ctx, s.UserID, s.ChatID, 20)
	report, _ = appendRecentMessages(report, userMessages)

	pushBanLog(ctx, s)
	sendReportToRecipients(ctx, b, s.ChatID, report,
		getMuteMessageKeyboard(s.ChatID, s.UserID, s.VoteMessageID), "textOnlyUser")

	if result {
		// do not notify if you failed
		params := &bot.SendMessageParams{
			ChatID:    s.ChatID,
			Text:      escape(fmt.Sprintf("Вам запрещена отправка картинок и стикеров на %s. Надеемся на понимание.", restrictionDurationText(userRecord))),
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
