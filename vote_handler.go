package main

import (
	"context"
	"fmt"
	"log"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// voteHandlerConfig describes how to resolve targets and any ban-specific extras.
type voteHandlerConfig struct {
	command       string
	getByUserID   func(ctx context.Context, chatID, userID int64) (*BanInfo, error)
	getByUsername func(ctx context.Context, chatID int64, username string) (*BanInfo, error)
	getByMessage  func(ctx context.Context, chatID, messageID int64) (*BanInfo, error)

	// usernameFallback is called when getByUsername fails (e.g. MTProto lookup for ban).
	// Returns nil, err when the user cannot be found; non-nil BanInfo on success.
	// When nil, the handler simply skips on a lookup failure.
	usernameFallback func(ctx context.Context, chatID int64, username string) (*BanInfo, error)

	// checkRecentCache guards against duplicate bans within the TTL window.
	checkRecentCache bool

	// trackVoteCount increments the requester's vote reputation counter.
	trackVoteCount bool
}

func makeVoteHandler(cfg voteHandlerConfig) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		chatId := update.Message.Chat.ID

		settingsMux.Lock()
		chatSettings := getChatSettings(ctx, chatId)
		senderId := update.Message.From.ID
		if update.Message.SenderChat != nil {
			senderId = update.Message.SenderChat.ID
		}
		if chatSettings.Pause {
			settingsMux.Unlock()
			onPauseMessage(ctx, b, update.Message)
			return
		}
		settingsMux.Unlock()

		if len(update.Message.Entities) == 1 && update.Message.Entities[0].Type != models.MessageEntityTypeBotCommand {
			systemAnswerToMessage(ctx, b, chatId, update.Message.ID,
				escape(fmt.Sprintf(
					"Укажите ссылку на сообщение или пользователя.\nПримеры:\n/%s https://t.me/c/1657123097/2854347\n/%s @username",
					cfg.command, cfg.command,
				)))
		}

		log.Printf("[%sHandler] new /%s from userID=%d in chatID=%d: %q",
			cfg.command, cfg.command, update.Message.From.ID, chatId, update.Message.Text)

		voteCount := 0

		for _, v := range update.Message.Entities {
			log.Printf("[%sHandler] entity type=%v text=%q in chatID=%d",
				cfg.command, v.Type, update.Message.Text[v.Offset:v.Offset+v.Length], chatId)

			var err error
			var banInfo *BanInfo

			switch v.Type {
			case models.MessageEntityTypeTextMention:
				log.Printf("[%sHandler] text mention: userID=%d firstName=%q in chatID=%d",
					cfg.command, v.User.ID, v.User.FirstName, chatId)
				banInfo, err = cfg.getByUserID(ctx, chatId, v.User.ID)
				if err != nil {
					log.Printf("[%sHandler] getByUserID failed for userID=%d in chatID=%d: %v",
						cfg.command, v.User.ID, chatId, err)
					continue
				}

			case models.MessageEntityTypeMention:
				username := update.Message.Text[v.Offset+1 : v.Offset+v.Length]
				log.Printf("[%sHandler] processing mention @%s in chatID=%d", cfg.command, username, chatId)
				if username == myID {
					continue
				}
				banInfo, err = cfg.getByUsername(ctx, chatId, username)
				if err != nil {
					log.Printf("[%sHandler] getByUsername failed for username=%q in chatID=%d: %v",
						cfg.command, username, chatId, err)
					if cfg.usernameFallback == nil {
						continue
					}
					banInfo, err = cfg.usernameFallback(ctx, chatId, username)
					if err != nil {
						systemAnswerToMessage(ctx, b, chatId, update.Message.ID,
							escape(fmt.Sprintf("Пользователь @%v не найден", username)), true)
						continue
					}
				}

			case models.MessageEntityTypeURL:
				log.Printf("[%sHandler] processing URL entity in chatID=%d: %q",
					cfg.command, chatId, update.Message.Text[v.Offset:v.Offset+v.Length])
				chatLinks := parseChatLink(update.Message.Text[v.Offset:v.Offset+v.Length], chatId, update.Message.Chat.Username)
				for _, chatLink := range chatLinks {
					if chatLink.err != nil {
						log.Printf("[%sHandler] failed to parse chat link in chatID=%d: %v",
							cfg.command, chatId, chatLink.err)
						continue
					}
					banInfo, err = cfg.getByMessage(ctx, chatId, chatLink.TargetMessageID)
					if err != nil {
						systemAnswerToMessage(ctx, b, chatId, update.Message.ID,
							escape(fmt.Sprintf("Сообщение не найдено. Используйте альтернативный метод: /%s @username", cfg.command)), true)
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

			if checkForDuplicates(ctx, chatId, banInfo.UserID, b, update) {
				continue
			}

			if cfg.checkRecentCache {
				sessionsMux.Lock()
				cached := getCachedBanInfo(banInfo.ChatID, banInfo.UserID)
				sessionsMux.Unlock()
				if cached {
					systemAnswerToMessage(ctx, b, chatId, update.Message.ID,
						"Пользователь уже был заблокирован недавно", true)
					continue
				}
			}

			log.Printf("[%sHandler] starting vote: userID=%d chatID=%d requiredScore=%d",
				cfg.command, banInfo.UserID, chatId, banInfo.Score)
			if !makeVoteMessage(ctx, banInfo, b) {
				continue
			}
			voteCount++
		}

		if cfg.trackVoteCount {
			userMakeVote(ctx, senderId, voteCount)
		}
	}
}

var banHandler = makeVoteHandler(voteHandlerConfig{
	command:       "ban",
	getByUserID:   getBanInfoByUserID,
	getByUsername: getBanInfoByUsername,
	getByMessage:  getBanInfo,
	usernameFallback: func(ctx context.Context, chatID int64, username string) (*BanInfo, error) {
		userInfo, err := client.GetUserByUsername(ctx, username)
		if err != nil {
			return nil, err
		}
		return getBanInfoByUserIDNoDB(chatID, userInfo.UserId, userInfo.Username), nil
	},
	checkRecentCache: true,
	trackVoteCount:   true,
})

var muteHandler = makeVoteHandler(voteHandlerConfig{
	command:       "mute",
	getByUserID:   getMuteInfoByUserID,
	getByUsername: getMuteInfoByUser,
	getByMessage:  getMuteInfo,
})

var textOnlyHandler = makeVoteHandler(voteHandlerConfig{
	command:       "text_only",
	getByUserID:   getTextOnlyInfoByUserID,
	getByUsername: getTextOnlyInfoByUser,
	getByMessage:  getTextOnlyInfo,
})
