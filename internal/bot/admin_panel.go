package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/ice2heart/poke_bot/internal/storage"
	"github.com/ice2heart/poke_bot/internal/tgutil"
	"go.uber.org/zap"
)

func (a *App) actionCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
		ShowAlert:       false,
	})
	// log.Println(len(update.CallbackQuery.Data))
	data, err := tgutil.Unmarshal(update.CallbackQuery.Data[2:])
	if err != nil {
		zap.S().Infof("[actionCallbackHandler] unmarshal failed for callbackData=%q: %v", update.CallbackQuery.Data, err)
		return
	}

	switch data.Action {
	case tgutil.ACTION_UNBAN:
		{
			if !a.isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[tgutil.DATA_TYPE_USERID]
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_UNBAN: missing userID in callback data, chatID=%d", data.ChatID)
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.CallbackQuery.From.ID,
					Text:   "Не удалось разблокировать пользователя",
					ReplyParameters: &models.ReplyParameters{
						ChatID:    update.CallbackQuery.From.ID,
						MessageID: update.CallbackQuery.Message.Message.ID,
					},
				})
				return
			}
			userID := tgutil.GetInt(userIdRaw)
			failUnban := func(msg string, args ...any) {
				zap.S().Infof(msg, args...)
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.CallbackQuery.From.ID,
					Text:   "Не удалось разблокировать пользователя",
					ReplyParameters: &models.ReplyParameters{
						ChatID:    update.CallbackQuery.From.ID,
						MessageID: update.CallbackQuery.Message.Message.ID,
					},
				})
			}

			ok, err := unbanUser(ctx, b, data.ChatID, userID)
			if err != nil {
				zap.S().Infof("[actionCallbackHandler] ACTION_UNBAN: Bot API failed for chatID=%d userID=%d: %v, trying MTProto", data.ChatID, userID, err)
				chatHash, hashErr := a.client.GetAccessHash(ctx, data.ChatID)
				if hashErr != nil {
					failUnban("[actionCallbackHandler] ACTION_UNBAN: GetAccessHash failed for chatID=%d: %v", data.ChatID, hashErr)
					return
				}
				mtUser, userErr := a.client.GetUser(ctx, userID)
				if userErr != nil {
					failUnban("[actionCallbackHandler] ACTION_UNBAN: GetUser failed for userID=%d: %v", userID, userErr)
					return
				}
				ok, err = a.client.UnbanUser(ctx, data.ChatID, chatHash, userID, mtUser.AccessHash)
				if err != nil {
					failUnban("[actionCallbackHandler] ACTION_UNBAN: MTProto UnbanUser failed for chatID=%d userID=%d: %v", data.ChatID, userID, err)
					return
				}
			}
			if !ok {
				failUnban("[actionCallbackHandler] ACTION_UNBAN: unban returned false for chatID=%d", data.ChatID)
				return
			}
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.CallbackQuery.From.ID,
				Text:   "Пользователь разблокирован",
				ReplyParameters: &models.ReplyParameters{
					ChatID:    update.CallbackQuery.From.ID,
					MessageID: update.CallbackQuery.Message.Message.ID,
				},
			})
		}
	case tgutil.ACTION_DELETE_ALL:
		{
			if !a.isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[tgutil.DATA_TYPE_USERID]
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_DELETE_ALL: missing userID in callback data, chatID=%d", data.ChatID)
				return
			}
			ok, err := deleteAllMessages(ctx, b, data.ChatID, tgutil.GetInt(userIdRaw))
			if err != nil {
				zap.S().Infof("[actionCallbackHandler] ACTION_DELETE_ALL: deleteAllMessages failed for chatID=%d: %v", data.ChatID, err)
				return
			}
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_DELETE_ALL: deleteAllMessages returned false for chatID=%d", data.ChatID)
				return
			}

		}
	case tgutil.ACTION_SHOW_CHAT_LIST:
		{
			zap.S().Infof("[actionCallbackHandler] ACTION_SHOW_CHAT_LIST: userID=%d", update.CallbackQuery.From.ID)
			chats := a.getChatsForAdmin(ctx, b, update.CallbackQuery.From.ID)
			zap.S().Infof("[actionCallbackHandler] ACTION_SHOW_CHAT_LIST: found %d chats for userID=%d", len(chats), update.CallbackQuery.From.ID)
			_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        "Control panel",
				ReplyMarkup: getChatListKeyboard(chats),
			})
			if err != nil {
				zap.S().Infof("[actionCallbackHandler] ACTION_SHOW_CHAT_LIST: EditMessageText failed: %v", err)
			}
		}
	case tgutil.ACTION_SHOW_CHAT_ID:
		{
			zap.S().Infof("[actionCallbackHandler] ACTION_SHOW_CHAT_ID: chatID=%d userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			chatName := getChatName(ctx, b, data.ChatID)
			b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        fmt.Sprintf("Управление чатом: %s", chatName),
				ReplyMarkup: getChatActionsKeyboard(data.ChatID),
			})
		}
	case tgutil.ACTION_PAUSE_CHAT:
		{
			zap.S().Infof("[actionCallbackHandler] ACTION_PAUSE_CHAT: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !a.isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}

			a.settingsMux.Lock()
			chatSettings := a.getChatSettings(ctx, data.ChatID)
			chatSettings.Pause = true
			a.settings[data.ChatID] = chatSettings
			storage.WriteChatSettings(ctx, data.ChatID, chatSettings)
			a.settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Режим паузы активирован", false, 30)
		}
	case tgutil.ACTION_UNPAUSE_CHAT:
		{
			zap.S().Infof("[actionCallbackHandler] ACTION_UNPAUSE_CHAT: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !a.isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}

			a.settingsMux.Lock()
			chatSettings := a.getChatSettings(ctx, data.ChatID)
			chatSettings.Pause = false
			a.settings[data.ChatID] = chatSettings
			storage.WriteChatSettings(ctx, data.ChatID, chatSettings)
			a.settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Режим паузы деактивирован", false, 30)
		}
	case tgutil.ACTION_ENABLED_LOG:
		{
			zap.S().Infof("[actionCallbackHandler] ACTION_ENABLED_LOG: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !a.isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userID := update.CallbackQuery.From.ID

			a.settingsMux.Lock()
			chatSettings := a.getChatSettings(ctx, data.ChatID)
			if slices.Contains(chatSettings.LogRecipients, userID) {
				a.settingsMux.Unlock()
				systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы уже в списке на получение отчётов", false, 30)
				return
			}
			chatSettings.LogRecipients = append(chatSettings.LogRecipients, userID)
			a.settings[data.ChatID] = chatSettings
			storage.WriteChatSettings(ctx, data.ChatID, chatSettings)
			a.settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы добавлены в список на получение отчётов", false, 30)
		}
	case tgutil.ACTION_DISABLED_LOG:
		{
			zap.S().Infof("[actionCallbackHandler] ACTION_DISABLED_LOG: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !a.isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userID := update.CallbackQuery.From.ID

			a.settingsMux.Lock()
			chatSettings := a.getChatSettings(ctx, data.ChatID)
			index := slices.Index(chatSettings.LogRecipients, userID)
			if index == -1 {
				a.settingsMux.Unlock()
				systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы не состоите в списке получателей отчётов", false, 30)
				return
			}
			chatSettings.LogRecipients = slices.Delete(chatSettings.LogRecipients, index, index+1)
			a.settings[data.ChatID] = chatSettings
			storage.WriteChatSettings(ctx, data.ChatID, chatSettings)
			a.settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы удалены из списка получателей отчётов", false, 30)

		}
	case tgutil.ACTION_UNMUTE:
		{
			if !a.isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[tgutil.DATA_TYPE_USERID]
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_UNMUTE: missing userID in callback data, chatID=%d", data.ChatID)
				return
			}
			zap.S().Infof("[actionCallbackHandler] ACTION_UNMUTE: userID=%d chatID=%d by userID=%d", tgutil.GetInt(userIdRaw), data.ChatID, update.CallbackQuery.From.ID)

			ok, err := unmuteUser(ctx, b, data.ChatID, tgutil.GetInt(userIdRaw))
			if err != nil {
				zap.S().Infof("[actionCallbackHandler] ACTION_UNMUTE: unmuteUser failed for chatID=%d: %v", data.ChatID, err)
				return
			}
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_UNMUTE: unmute returned false for chatID=%d", data.ChatID)
				return
			}
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.CallbackQuery.From.ID,
				Text:   "Ограничения пользователя сняты",
				ReplyParameters: &models.ReplyParameters{
					ChatID:    update.CallbackQuery.From.ID,
					MessageID: update.CallbackQuery.Message.Message.ID,
				},
			})

		}
	case tgutil.ACTION_SHOW_VOTERS:
		{
			if !a.isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			msgIdRaw, ok := data.Data[tgutil.DATA_TYPE_MSGID]
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_SHOW_VOTERS: missing messageID in callback data, chatID=%d", data.ChatID)
				return
			}
			voteMessageID := tgutil.GetInt(msgIdRaw)
			zap.S().Infof("[actionCallbackHandler] ACTION_SHOW_VOTERS: chatID=%d voteMessageID=%d by userID=%d", data.ChatID, voteMessageID, update.CallbackQuery.From.ID)

			text := "Информация о голосовании не найдена"
			banLog, err := storage.GetBanLogByVoteMessage(ctx, data.ChatID, voteMessageID)
			if err == nil {
				text = formatVotersReport(&BanInfo{BanLogRecord: *banLog}, func(uID int64) string {
					return userTagByID(ctx, uID)
				})
			}
			_, err = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:    update.CallbackQuery.Message.Message.Chat.ID,
				Text:      text,
				ParseMode: models.ParseModeMarkdown,
				ReplyParameters: &models.ReplyParameters{
					ChatID:    update.CallbackQuery.Message.Message.Chat.ID,
					MessageID: update.CallbackQuery.Message.Message.ID,
				},
				LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
			})
			if err != nil {
				zap.S().Infof("[actionCallbackHandler] ACTION_SHOW_VOTERS: SendMessage failed for chatID=%d: %v", data.ChatID, err)
			}
		}
	case tgutil.ACTION_LIKES_PAGE:
		{
			pageRaw, ok := data.Data[tgutil.DATA_TYPE_PAGE]
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_LIKES_PAGE: missing page in callback data, chatID=%d", data.ChatID)
				return
			}
			page := int(tgutil.GetInt(pageRaw))
			zap.S().Infof("[actionCallbackHandler] ACTION_LIKES_PAGE: chatID=%d page=%d by userID=%d", data.ChatID, page, update.CallbackQuery.From.ID)

			text, kb := a.renderLikesPage(ctx, data.ChatID, page)
			_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        text,
				ParseMode:   models.ParseModeMarkdown,
				ReplyMarkup: kb,
			})
			if err != nil {
				zap.S().Infof("[actionCallbackHandler] ACTION_LIKES_PAGE: EditMessageText failed: %v", err)
			}
		}
	case tgutil.ACTION_LEAVE_CHAT:
		{
			if !a.isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			zap.S().Infof("[actionCallbackHandler] ACTION_LEAVE_CHAT: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: data.ChatID,
				Text:   "Покидаю чат. До свидания!",
			})
			_, err := b.LeaveChat(ctx, &bot.LeaveChatParams{
				ChatID: data.ChatID,
			})
			if err != nil {
				zap.S().Infof("Can't leave chat %d : %v", data.ChatID, err)
				return
			}

			// return to list of chats
			chats := a.getChatsForAdmin(ctx, b, update.CallbackQuery.From.ID)
			zap.S().Infof("[actionCallbackHandler] ACTION_LEAVE_CHAT: refreshed %d chats for userID=%d", len(chats), update.CallbackQuery.From.ID)
			b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        "Control panel",
				ReplyMarkup: getChatListKeyboard(chats),
			})

		}
	}
}

func testHandler(ctx context.Context, b *bot.Bot, update *models.Update) {

	zap.S().Infof("[testHandler] chatID=%d userID=%d", update.Message.Chat.ID, update.Message.From.ID)
	// chatId := update.Message.Chat.ID
	// publicInt, _ := strconv.ParseInt(makePublicGroupString(chatId), 10, 64)

	jcart, _ := json.MarshalIndent(update, "", "\t")
	fmt.Println(string(jcart))

	// chatinfo, err := b.GetChatAdministrators(ctx, &bot.GetChatAdministratorsParams{
	// 	ChatID: update.Message.Chat.ID,
	// })

	// if err == nil {
	// 	jcart, _ := json.MarshalIndent(chatinfo, "", "\t")
	// 	log.Println(string(jcart))
	// 	return
	// }

	// testData := &Item{
	// 	Action: 1,
	// 	ChatID: publicInt,
	// 	Data:   map[uint8]interface{}{1: 23, 4: 4342},

	// 	// Data: "test",
	// }

	// enc, err := marshal(testData)
	// if err != nil {
	// 	log.Printf("Shit happens %v", err)
	// 	return
	// }

	// log.Printf("Button data %s, length %d\n", enc, len(enc))

	// _, err = b.SendMessage(ctx, &bot.SendMessageParams{
	// 	ChatID:    chatId,
	// 	Text:      "Test test",
	// 	ParseMode: models.ParseModeMarkdown,
	// 	ReplyMarkup: &models.InlineKeyboardMarkup{
	// 		InlineKeyboard: [][]models.InlineKeyboardButton{
	// 			{
	// 				{Text: "testbtn", CallbackData: fmt.Sprintf("b_%s", enc)},
	// 			},
	// 		},
	// 	},
	// })
	// if err != nil {
	// 	log.Println(err)
	// }
}

// TODO: have to be a better func
func (a *App) getChatsForAdmin(ctx context.Context, b *bot.Bot, userID int64) []Chat {
	chats := make([]Chat, 0, 4)

	a.adminsMux.Lock()
	defer a.adminsMux.Unlock()

	for k, v := range a.admins {
		_, ok := v[userID]
		if !ok && userID != a.superAdminID {
			continue
		}
		name := getChatName(ctx, b, k)
		chats = append(chats, Chat{
			ChatID:   k,
			ChatName: name,
		})
	}
	return chats
}

func (a *App) startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID

	if chatID == userID {
		// admin menu
		chats := a.getChatsForAdmin(ctx, b, userID)
		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        "Control panel",
			ParseMode:   models.ParseModeMarkdown,
			ReplyMarkup: getChatListKeyboard(chats),
		})
		if err != nil {
			zap.S().Info(err)
		}
		return
	}
	// chat menu
	return

}

func (a *App) deleteMessageHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	a.adminsMux.Lock()
	chatAdmins := a.checkAdmins(ctx, b, update.Message.Chat.ID)
	_, rep := chatAdmins[update.Message.From.ID]
	a.adminsMux.Unlock()

	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    update.Message.Chat.ID,
		MessageID: update.Message.ID,
	})
	if !rep {
		return
	}
	chatId := update.Message.Chat.ID
	for _, msgID := range extractLinkedMessageIDs(update.Message.Entities, update.Message.Text, chatId, update.Message.Chat.Username) {
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    chatId,
			MessageID: msgID,
		})
	}
}
