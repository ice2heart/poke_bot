package bot

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/ice2heart/poke_bot/internal/storage"
	"github.com/ice2heart/poke_bot/internal/tgutil"
	"go.uber.org/zap"
)

func makeMuteMessage(b *BanInfo) string {
	return makeVoteText(b, "мут")
}

func (a *App) getMuteInfoByUserID(ctx context.Context, chatID int64, userID int64) (*BanInfo, error) {
	return a.getInfoByUserID(ctx, chatID, userID, MUTE, makeMuteMessage)
}

func (a *App) getMuteInfoByUser(ctx context.Context, chatID int64, username string) (*BanInfo, error) {
	user, err := storage.GetRatingFromUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return a.getMuteInfoByUserID(ctx, chatID, user.Userid)
}

func getMuteInfo(ctx context.Context, chatID int64, messageID int64) (*BanInfo, error) {
	return getInfoByMessage(ctx, chatID, messageID, MUTE, makeMuteMessage)
}

func (a *App) muteUser(ctx context.Context, b *bot.Bot, s *BanInfo) bool {
	zap.S().Infof("[muteUser] start: userID=%d chatID=%d targetMessageID=%d", s.UserID, s.ChatID, s.TargetMessageID)

	user, err := storage.GetUser(ctx, s.UserID)
	var userRecord storage.UserRecord
	var banUsertag string

	if err == nil {
		userRecord = *user
		banUsertag = toClickableUsername(user)
	} else {
		banUsertag = fmt.Sprintf("[Пользователь вне базы](tg://user?id=%d)", s.UserID)
	}

	zap.S().Infof("[muteUser] restricting: userID=%d chatID=%d duration=%d days", s.UserID, s.ChatID, restrictionDurationInDays(userRecord))
	result, err := b.RestrictChatMember(ctx, &bot.RestrictChatMemberParams{
		ChatID:    s.ChatID,
		UserID:    s.UserID,
		UntilDate: restrictionUntilDate(userRecord),
		Permissions: &models.ChatPermissions{
			CanSendOtherMessages:  false,
			CanAddWebPagePreviews: false,
			CanSendPolls:          false,
		},
		UseIndependentChatPermissions: false,
	})
	zap.S().Infof("[muteUser] RestrictChatMember result=%v err=%v", result, err)
	if err != nil {
		zap.S().Infof("[muteUser] RestrictChatMember failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
	}

	if result {
		err = storage.UserAddMuteCounter(ctx, s.UserID)
		if err != nil {
			zap.S().Infof("[muteUser] userAddMuteCounter failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
		}
	}

	deleteMessagesConcurrently(ctx, b, s.ChatID, []int64{s.VoteMessageID, s.RequestMessageID}, "muteUser")

	resultText := fmt.Sprintf("Мут выдан на %s", restrictionDurationText(userRecord))
	if !result {
		resultText = "Не удалось выдать мут"
	}
	report := a.buildModerationReport(ctx, s.ChatID, s.OwnerID, resultText, banUsertag)

	userMessages, _ := storage.GetUserLastNthMessages(ctx, s.UserID, s.ChatID, 20)
	report, _ = appendRecentMessages(report, userMessages)

	storage.PushBanLog(ctx, &s.BanLogRecord)
	a.sendReportToRecipients(ctx, b, s.ChatID, report,
		getMuteMessageKeyboard(s.ChatID, s.UserID, s.VoteMessageID), "muteUser")

	if result {
		// do not notify if you failed
		notifyText := tgutil.Escape(fmt.Sprintf("Вам выдан мут на %s. Надеемся на понимание.", restrictionDurationText(userRecord)))

		var replyParams *models.ReplyParameters
		if s.TargetMessageID != 0 {
			replyParams = &models.ReplyParameters{
				ChatID:    s.ChatID,
				MessageID: int(s.TargetMessageID),
			}
		}

		zap.S().Infof("[muteUser] sending chat notification: chatID=%d targetMessageID=%d cccp=%v", s.ChatID, s.TargetMessageID, s.Cccp)

		var notifyErr error
		if s.Cccp {
			_, notifyErr = a.sendImage(ctx, b, "cccp", s.ChatID, &bot.SendPhotoParams{
				ChatID:          s.ChatID,
				Caption:         notifyText,
				ParseMode:       models.ParseModeMarkdown,
				ReplyParameters: replyParams,
			})
		} else {
			_, notifyErr = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          s.ChatID,
				Text:            notifyText,
				ParseMode:       models.ParseModeMarkdown,
				ReplyParameters: replyParams,
			})
		}
		if notifyErr != nil {
			zap.S().Infof("[muteUser] chat notification failed: chatID=%d cccp=%v: %v", s.ChatID, s.Cccp, notifyErr)
		}

	}

	return result
}
