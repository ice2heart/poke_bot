package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

var (
	publicGroupRX = regexp.MustCompile(`^-100`)
)

type Chat struct {
	ChatID   int64
	ChatName string
}

func makePublicGroupString(groupID int64) string {
	// https://gist.github.com/nafiesl/4ad622f344cd1dc3bb1ecbe468ff9f8a
	// groudid has leeading -100
	return publicGroupRX.ReplaceAllString(strconv.FormatInt(groupID, 10), "")
}

func getVoteButtons(upvotes int, downvotes int) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: fmt.Sprintf("Бан! (%d)", upvotes), CallbackData: "button_upvote"},
				{Text: fmt.Sprintf("Не бан (%d)", downvotes), CallbackData: "button_downvote"},
			},
		},
	}
}

func getBanMessageKeyboard(chatId int64, userId int64) *models.InlineKeyboardMarkup {
	unbanData, err := marshal(&Item{
		Action: ACTION_UNBAN,
		ChatID: chatId,
		Data:   map[uint8]interface{}{DATA_TYPE_USERID: userId},
	})

	if err != nil {
		log.Printf("Can't make unban data %v", err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}

	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "Разбанить", CallbackData: fmt.Sprintf("b_%s", unbanData)},
			},
		},
	}
}

func getChatListKeyboard(chatList []Chat) *models.InlineKeyboardMarkup {
	buttons := make([][]models.InlineKeyboardButton, len(chatList)+1)
	for k, v := range chatList {
		showChat, err := marshal(&Item{
			Action: ACTION_SHOW_CHAT_ID,
			ChatID: v.ChatID,
		})
		if err != nil {
			log.Printf("Make chat data error: %v", err)
			continue
		}
		buttons[k] = []models.InlineKeyboardButton{{Text: v.ChatName, CallbackData: fmt.Sprintf("b_%s", showChat)}}
	}
	refresh, err := marshal(&Item{
		Action: ACTION_SHOW_CHAT_LIST,
	})
	if err != nil {
		log.Printf("Make chat data error: %v", err)
		return nil
	}
	buttons[len(chatList)] = []models.InlineKeyboardButton{{Text: "🗘 обновить", CallbackData: fmt.Sprintf("b_%s", refresh)}}
	return &models.InlineKeyboardMarkup{InlineKeyboard: buttons}

}

func getChatActionsKeyboard(chatID int64) *models.InlineKeyboardMarkup {
	pauseChat, err := marshal(&Item{
		Action: ACTION_PAUSE_CHAT,
		ChatID: chatID,
	})
	if err != nil {
		log.Printf("Can't make a pause button %v", err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	unPauseChat, err := marshal(&Item{
		Action: ACTION_UNPAUSE_CHAT,
		ChatID: chatID,
	})
	if err != nil {
		log.Printf("Can't make a unpause button %v", err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	enableLog, err := marshal(&Item{
		Action: ACTION_ENABLED_LOG,
		ChatID: chatID,
	})
	if err != nil {
		log.Printf("Can't make a enable log button %v", err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	disableLog, err := marshal(&Item{
		Action: ACTION_DISABLED_LOG,
		ChatID: chatID,
	})
	if err != nil {
		log.Printf("Can't make a disble log button %v", err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	refresh, err := marshal(&Item{
		Action: ACTION_SHOW_CHAT_LIST,
	})
	if err != nil {
		log.Printf("Can't make a back button %v", err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "Поставить чат на паузу", CallbackData: fmt.Sprintf("b_%s", pauseChat)},
				{Text: "Снять чат с паузы", CallbackData: fmt.Sprintf("b_%s", unPauseChat)},
			},
			{
				{Text: "Включить логирование", CallbackData: fmt.Sprintf("b_%s", enableLog)},
				{Text: "Выключить логирование", CallbackData: fmt.Sprintf("b_%s", disableLog)},
			},
			{
				{Text: "К списку чатов", CallbackData: fmt.Sprintf("b_%s", refresh)},
			},
		},
	}

}

func getChatName(ctx context.Context, b *bot.Bot, chatID int64) string {
	// TODO: make it cacheble
	chatInfo, err := b.GetChat(ctx, &bot.GetChatParams{
		ChatID: chatID,
	})
	if err != nil {
		return fmt.Sprintf("Unknown chat with id %d", chatID)
	}
	return chatInfo.Title
}

func getAdmins(ctx context.Context, b *bot.Bot, chat int64) (ret map[int64]bool) {

	admins, err := b.GetChatAdministrators(ctx, &bot.GetChatAdministratorsParams{
		ChatID: chat,
	})
	ret = make(map[int64]bool)
	if err != nil {
		log.Printf("Can't get chat %d admins: %v", chat, err)
		return
	}
	for _, admin := range admins {
		switch admin.Type {
		case models.ChatMemberTypeAdministrator:
			ret[admin.Administrator.User.ID] = true
		case models.ChatMemberTypeOwner:
			ret[admin.Owner.User.ID] = true
		default:
			log.Printf("Some strange type here %v", admin.Type)
		}
	}
	return ret
}

func systemAnswerToMessage(ctx context.Context, b *bot.Bot, chatId int64, messageId int, text string, deleteOrigin ...bool) {

	reply, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatId,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
		ReplyParameters: &models.ReplyParameters{
			ChatID:    chatId,
			MessageID: messageId,
		},
	})
	if err != nil {
		log.Printf("error: Can't send message to chatid %d messageId %d: %v", chatId, messageId, err)
		return
	}
	removeChatID := chatId
	removeReplyID := reply.ID
	removeOriginalID := messageId
	go dealy(ctx, 30, func() {
		if len(deleteOrigin) == 0 {
			b.DeleteMessage(ctx, &bot.DeleteMessageParams{
				ChatID:    removeChatID,
				MessageID: removeOriginalID,
			})
		}
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    removeChatID,
			MessageID: removeReplyID,
		})
	})
}

func unbanUser(ctx context.Context, b *bot.Bot, chatId int64, userId int64) (result bool, err error) {
	result, err = b.UnbanChatMember(ctx, &bot.UnbanChatMemberParams{
		ChatID:       chatId,
		UserID:       userId,
		OnlyIfBanned: true,
	})
	return
}

func deleteAllMessages(ctx context.Context, b *bot.Bot, chatId int64, userId int64) (result bool, err error) {

	return true, nil
}

func isUserAdmin(ctx context.Context, b *bot.Bot, chatID int64, userID int64, messageChatID int64, messageID int) bool {
	chatAdmins := checkAdmins(ctx, b, chatID)
	_, rep := chatAdmins[userID]
	if !rep {
		log.Printf("User %d try to use admin prev for chat %d", userID, chatID)
		systemAnswerToMessage(ctx, b, messageChatID, messageID, "Необходимо быть админом для чата")
		return false
	}
	return true
}
