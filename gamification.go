package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

func bestHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID

	if !isUserAdmin(ctx, b, chatID, userID, chatID, update.Message.ID) {
		return
	}

	adminsMux.Lock()
	chatAdmins := checkAdmins(ctx, b, chatID)
	adminsMux.Unlock()

	topUsers, err := getTopUsersByVotes(ctx, 100)
	if err != nil {
		zap.S().Infof("[bestHandler] getTopUsersByVotes failed: %v", err)
		return
	}

	lines := make([]string, 0, 20)
	rank := 1
	for _, user := range topUsers {
		if _, isAdmin := chatAdmins[user.Uid]; isAdmin {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d\\. %s : %d", rank, user.toClickableUsername(), user.VoteCounter))
		rank++
		if rank > 20 {
			break
		}
	}

	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    chatID,
		MessageID: update.Message.ID,
	})

	if len(lines) == 0 {
		systemMessage(ctx, b, chatID, "Нет данных", 30)
		return
	}

	_, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      strings.Join(lines, "\n"),
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		zap.S().Infof("[bestHandler] SendMessage failed chatID=%d: %v", chatID, err)
	}
}
