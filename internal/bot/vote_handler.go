package bot

import (
	"context"
	"fmt"
	"github.com/ice2heart/poke_bot/internal/storage"
	"regexp"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/ice2heart/poke_bot/internal/tgutil"
	"go.uber.org/zap"
)

var tgUserLinkRegex *regexp.Regexp = regexp.MustCompile(`^tg://user\?id=(\d+)$`)

// cccpRegex matches the /mute^cccp marker in either the Latin or the Cyrillic
// spelling — the two are homoglyphs, visually identical. The marker is '^'
// rather than '@' because @cccp is a claimable Telegram username, which would
// make the image fire whenever that user was muted.
var cccpRegex *regexp.Regexp = regexp.MustCompile(`(?i)\^(cccp|ссср)`)

// voteHandlerConfig describes how to resolve targets and any ban-specific extras.
type voteHandlerConfig struct {
	command       string
	getByUserID   func(ctx context.Context, chatID, userID int64) (*BanInfo, error)
	getByUsername func(ctx context.Context, chatID int64, username string) (*BanInfo, error)
	getByMessage  func(ctx context.Context, chatID, messageID int64) (*BanInfo, error)

	// checkRecentCache guards against duplicate bans within the TTL window.
	checkRecentCache bool

	// allowCccp enables the /<command>^cccp variant, which attaches cccp.jpg to
	// the notification sent when the vote passes.
	allowCccp bool
}

// newBanInfoNoDB builds a BanInfo for a user that is not in the database.
// banType and makeMessage are caller-supplied so the function works for any
// action type (ban, mute, text-only).
func newBanInfoNoDB(chatID, userID int64, username string, banType uint8, makeMessage func(*BanInfo) string) *BanInfo {
	banInfo := &BanInfo{BanLogRecord: storage.BanLogRecord{
		ChatID:      chatID,
		UserID:      userID,
		UserName:    username,
		Score:       LOW_SCORE,
		LastMessage: "Сообщение не найдено",
		Type:        banType,
	}}
	banInfo.BanMessage = makeMessage(banInfo)
	return banInfo
}

func (a *App) makeVoteHandler(cfg voteHandlerConfig) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		chatId := update.Message.Chat.ID

		a.settingsMux.Lock()
		chatSettings := a.getChatSettings(ctx, chatId)
		senderId := update.Message.From.ID
		if update.Message.SenderChat != nil {
			senderId = update.Message.SenderChat.ID
		}
		if chatSettings.Pause {
			a.settingsMux.Unlock()
			onPauseMessage(ctx, b, update.Message)
			return
		}
		linkedChannelUsername := chatSettings.LinkedChannelUsername
		a.settingsMux.Unlock()

		if len(update.Message.Entities) == 0 || (len(update.Message.Entities) == 1 && update.Message.Entities[0].Type == models.MessageEntityTypeBotCommand) {
			systemAnswerToMessage(ctx, b, chatId, update.Message.ID,
				tgutil.Escape(fmt.Sprintf(
					"Укажите ссылку на сообщение или пользователя.\nПримеры:\n/%s https://t.me/c/1657123097/2854347\n/%s @username\n/%s https://t.me/channelname/123?comment=456",
					cfg.command, cfg.command, cfg.command,
				)), true, 30)
			return
		}

		zap.S().Infof("[%sHandler] new /%s from userID=%d in chatID=%d: %q",
			cfg.command, cfg.command, update.Message.From.ID, chatId, update.Message.Text)

		for _, v := range update.Message.Entities {
			zap.S().Infof("[%sHandler] entity type=%v text=%q in chatID=%d",
				cfg.command, v.Type, tgutil.EntityText(update.Message.Text, v.Offset, v.Length), chatId)

			var err error
			var banInfo *BanInfo

			switch v.Type {
			case models.MessageEntityTypeTextMention:
				zap.S().Infof("[%sHandler] text mention: userID=%d firstName=%q in chatID=%d",
					cfg.command, v.User.ID, v.User.FirstName, chatId)
				banInfo, err = cfg.getByUserID(ctx, chatId, v.User.ID)
				if err != nil {
					zap.S().Infof("[%sHandler] getByUserID failed for userID=%d in chatID=%d: %v",
						cfg.command, v.User.ID, chatId, err)
					continue
				}

			case models.MessageEntityTypeMention:
				username := tgutil.EntityText(update.Message.Text, v.Offset+1, v.Length-1)
				zap.S().Infof("[%sHandler] processing mention @%s in chatID=%d", cfg.command, username, chatId)
				if username == a.myID {
					continue
				}
				banInfo, err = cfg.getByUsername(ctx, chatId, username)
				if err != nil {
					zap.S().Infof("[%sHandler] getByUsername failed for username=%q in chatID=%d: %v",
						cfg.command, username, chatId, err)
					systemAnswerToMessage(ctx, b, chatId, update.Message.ID,
						tgutil.Escape(fmt.Sprintf("Пользователь @%v не найден", username)), true, 30)
					continue
				}

			case models.MessageEntityTypeURL:
				rawURL := tgutil.EntityText(update.Message.Text, v.Offset, v.Length)
				zap.S().Infof("[%sHandler] processing URL entity in chatID=%d: %q", cfg.command, chatId, rawURL)
				if m := tgUserLinkRegex.FindStringSubmatch(rawURL); m != nil {
					userID, err := strconv.ParseInt(m[1], 10, 64)
					if err != nil {
						zap.S().Infof("[%sHandler] failed to parse userID from tg://user link in chatID=%d: %v", cfg.command, chatId, err)
						continue
					}
					zap.S().Infof("[%sHandler] tg://user link: userID=%d in chatID=%d", cfg.command, userID, chatId)
					banInfo, err = cfg.getByUserID(ctx, chatId, userID)
					if err != nil {
						zap.S().Infof("[%sHandler] getByUserID failed for userID=%d in chatID=%d: %v", cfg.command, userID, chatId, err)
					}
					break
				}
				chatLinks := tgutil.ParseChatLink(rawURL, chatId, update.Message.Chat.Username, linkedChannelUsername)
				for _, chatLink := range chatLinks {
					if chatLink.Err != nil {
						zap.S().Infof("[%sHandler] failed to parse chat link in chatID=%d: %v",
							cfg.command, chatId, chatLink.Err)
						continue
					}
					banInfo, err = cfg.getByMessage(ctx, chatId, chatLink.TargetMessageID)
					if err != nil {
						systemAnswerToMessage(ctx, b, chatId, update.Message.ID,
							tgutil.Escape(fmt.Sprintf("Сообщение не найдено. Используйте альтернативный метод: /%s @username", cfg.command)), true, 30)
						continue
					}
					banInfo.TargetMessageID = chatLink.TargetMessageID
				}
			}

			if banInfo == nil {
				continue
			}

			banInfo.OwnerID = senderId
			banInfo.RequestMessageID = int64(update.Message.ID)
			banInfo.Cccp = cfg.allowCccp && cccpRegex.MatchString(update.Message.Text)

			if a.checkForDuplicates(ctx, chatId, banInfo.UserID, senderId, b, update) {
				continue
			}

			if cfg.checkRecentCache {
				if a.banCache.getCachedBanInfo(banInfo.ChatID, banInfo.UserID) {
					systemAnswerToMessage(ctx, b, chatId, update.Message.ID,
						"Пользователь уже был заблокирован недавно", true, 30)
					continue
				}
			}

			zap.S().Infof("[%sHandler] starting vote: userID=%d chatID=%d requiredScore=%d",
				cfg.command, banInfo.UserID, chatId, banInfo.Score)
			if !a.makeVoteMessage(ctx, banInfo, b) {
				continue
			}
		}
	}
}

// newVoteHandlers creates the ban, mute and text_only command handlers.
func (a *App) newVoteHandlers() {
	a.banHandler = a.makeVoteHandler(voteHandlerConfig{
		command:          "ban",
		getByUserID:      a.getBanInfoByUserID,
		getByUsername:    a.getBanInfoByUsername,
		getByMessage:     getBanInfo,
		checkRecentCache: true,
	})

	a.muteHandler = a.makeVoteHandler(voteHandlerConfig{
		command:       "mute",
		getByUserID:   a.getMuteInfoByUserID,
		getByUsername: a.getMuteInfoByUser,
		getByMessage:  getMuteInfo,
		allowCccp:     true,
	})

	a.textOnlyHandler = a.makeVoteHandler(voteHandlerConfig{
		command:       "text_only",
		getByUserID:   a.getTextOnlyInfoByUserID,
		getByUsername: a.getTextOnlyInfoByUser,
		getByMessage:  getTextOnlyInfo,
	})
}
