package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

func actionCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
		ShowAlert:       false,
	})
	// log.Println(len(update.CallbackQuery.Data))
	data, err := unmarshal(update.CallbackQuery.Data[2:])
	if err != nil {
		zap.S().Infof("[actionCallbackHandler] unmarshal failed for callbackData=%q: %v", update.CallbackQuery.Data, err)
		return
	}

	switch data.Action {
	case ACTION_UNBAN:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[DATA_TYPE_USERID]
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
			userID := getInt(userIdRaw)
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
				chatHash, hashErr := client.GetAccessHash(ctx, data.ChatID)
				if hashErr != nil {
					failUnban("[actionCallbackHandler] ACTION_UNBAN: GetAccessHash failed for chatID=%d: %v", data.ChatID, hashErr)
					return
				}
				mtUser, userErr := client.GetUser(ctx, userID)
				if userErr != nil {
					failUnban("[actionCallbackHandler] ACTION_UNBAN: GetUser failed for userID=%d: %v", userID, userErr)
					return
				}
				ok, err = client.UnbanUser(ctx, data.ChatID, chatHash, userID, mtUser.AccessHash)
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
	case ACTION_DELETE_ALL:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[DATA_TYPE_USERID]
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_DELETE_ALL: missing userID in callback data, chatID=%d", data.ChatID)
				return
			}
			ok, err := deleteAllMessages(ctx, b, data.ChatID, getInt(userIdRaw))
			if err != nil {
				zap.S().Infof("[actionCallbackHandler] ACTION_DELETE_ALL: deleteAllMessages failed for chatID=%d: %v", data.ChatID, err)
				return
			}
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_DELETE_ALL: deleteAllMessages returned false for chatID=%d", data.ChatID)
				return
			}

		}
	case ACTION_SHOW_CHAT_LIST:
		{
			zap.S().Infof("[actionCallbackHandler] ACTION_SHOW_CHAT_LIST: userID=%d", update.CallbackQuery.From.ID)
			chats := getChatsForAdmin(ctx, b, update.CallbackQuery.From.ID)
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
	case ACTION_SHOW_CHAT_ID:
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
	case ACTION_PAUSE_CHAT:
		{
			zap.S().Infof("[actionCallbackHandler] ACTION_PAUSE_CHAT: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}

			settingsMux.Lock()
			chatSettings := getChatSettings(ctx, data.ChatID)
			chatSettings.Pause = true
			settings[data.ChatID] = chatSettings
			writeChatSettings(ctx, data.ChatID, chatSettings)
			settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Режим паузы активирован", false, 30)
		}
	case ACTION_UNPAUSE_CHAT:
		{
			zap.S().Infof("[actionCallbackHandler] ACTION_UNPAUSE_CHAT: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}

			settingsMux.Lock()
			chatSettings := getChatSettings(ctx, data.ChatID)
			chatSettings.Pause = false
			settings[data.ChatID] = chatSettings
			writeChatSettings(ctx, data.ChatID, chatSettings)
			settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Режим паузы деактивирован", false, 30)
		}
	case ACTION_ENABLED_LOG:
		{
			zap.S().Infof("[actionCallbackHandler] ACTION_ENABLED_LOG: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userID := update.CallbackQuery.From.ID

			settingsMux.Lock()
			chatSettings := getChatSettings(ctx, data.ChatID)
			if slices.Contains(chatSettings.LogRecipients, userID) {
				settingsMux.Unlock()
				systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы уже в списке на получение отчётов", false, 30)
				return
			}
			chatSettings.LogRecipients = append(chatSettings.LogRecipients, userID)
			settings[data.ChatID] = chatSettings
			writeChatSettings(ctx, data.ChatID, chatSettings)
			settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы добавлены в список на получение отчётов", false, 30)
		}
	case ACTION_DISABLED_LOG:
		{
			zap.S().Infof("[actionCallbackHandler] ACTION_DISABLED_LOG: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userID := update.CallbackQuery.From.ID

			settingsMux.Lock()
			chatSettings := getChatSettings(ctx, data.ChatID)
			index := slices.Index(chatSettings.LogRecipients, userID)
			if index == -1 {
				settingsMux.Unlock()
				systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы не состоите в списке получателей отчётов", false, 30)
				return
			}
			chatSettings.LogRecipients = slices.Delete(chatSettings.LogRecipients, index, index+1)
			settings[data.ChatID] = chatSettings
			writeChatSettings(ctx, data.ChatID, chatSettings)
			settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы удалены из списка получателей отчётов", false, 30)

		}
	case ACTION_UNMUTE:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[DATA_TYPE_USERID]
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_UNMUTE: missing userID in callback data, chatID=%d", data.ChatID)
				return
			}
			zap.S().Infof("[actionCallbackHandler] ACTION_UNMUTE: userID=%d chatID=%d by userID=%d", getInt(userIdRaw), data.ChatID, update.CallbackQuery.From.ID)

			ok, err := unmuteUser(ctx, b, data.ChatID, getInt(userIdRaw))
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
	case ACTION_SHOW_VOTERS:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			msgIdRaw, ok := data.Data[DATA_TYPE_MSGID]
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_SHOW_VOTERS: missing messageID in callback data, chatID=%d", data.ChatID)
				return
			}
			voteMessageID := getInt(msgIdRaw)
			zap.S().Infof("[actionCallbackHandler] ACTION_SHOW_VOTERS: chatID=%d voteMessageID=%d by userID=%d", data.ChatID, voteMessageID, update.CallbackQuery.From.ID)

			text := "Информация о голосовании не найдена"
			banInfo, err := getBanLogByVoteMessage(ctx, data.ChatID, voteMessageID)
			if err == nil {
				text = formatVotersReport(banInfo, func(uID int64) string {
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
	case ACTION_LIKES_PAGE:
		{
			pageRaw, ok := data.Data[DATA_TYPE_PAGE]
			if !ok {
				zap.S().Infof("[actionCallbackHandler] ACTION_LIKES_PAGE: missing page in callback data, chatID=%d", data.ChatID)
				return
			}
			page := int(getInt(pageRaw))
			zap.S().Infof("[actionCallbackHandler] ACTION_LIKES_PAGE: chatID=%d page=%d by userID=%d", data.ChatID, page, update.CallbackQuery.From.ID)

			text, kb := renderLikesPage(ctx, data.ChatID, page)
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
	case ACTION_LEAVE_CHAT:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
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
			chats := getChatsForAdmin(ctx, b, update.CallbackQuery.From.ID)
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
func getChatsForAdmin(ctx context.Context, b *bot.Bot, userID int64) []Chat {
	chats := make([]Chat, 0, 4)

	adminsMux.Lock()
	defer adminsMux.Unlock()

	for k, v := range admins {
		_, ok := v[userID]
		if !ok && userID != superAdminID {
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

func startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID

	if chatID == userID {
		// admin menu
		chats := getChatsForAdmin(ctx, b, userID)
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

func deleteMessageHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	adminsMux.Lock()
	chatAdmins := checkAdmins(ctx, b, update.Message.Chat.ID)
	_, rep := chatAdmins[update.Message.From.ID]
	adminsMux.Unlock()

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
