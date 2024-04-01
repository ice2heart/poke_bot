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

func systemAnswerToMessage(ctx context.Context, b *bot.Bot, chatId int64, messageId int, text string) {
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
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    removeChatID,
			MessageID: removeOriginalID,
		})
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    removeChatID,
			MessageID: removeReplyID,
		})
	})
}
