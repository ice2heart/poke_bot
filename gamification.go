package main

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

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

const CUSTOM_TAG_MAX_LENGTH = 16

func setTagHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	messageID := update.Message.ID

	if update.Message.SenderChat != nil {
		systemAnswerToMessage(ctx, b, chatID, messageID, "Команда недоступна для каналов.", true, 30)
		return
	}
	userID := update.Message.From.ID

	_, tag, _ := strings.Cut(update.Message.Text, " ")
	tag = strings.TrimSpace(tag)
	if utf8.RuneCountInString(tag) > CUSTOM_TAG_MAX_LENGTH {
		systemAnswerToMessage(ctx, b, chatID, messageID, fmt.Sprintf("Слишком длинный тег, максимум %d символов.", CUSTOM_TAG_MAX_LENGTH), true, 30)
		return
	}

	score, err := getRatingFromUserID(ctx, userID)
	if err != nil || score.Rating <= CUSTOM_TAG_MIN_RATING {
		rating := 0
		if score != nil {
			rating = score.Rating
		}
		systemAnswerToMessage(ctx, b, chatID, messageID, fmt.Sprintf("Недостаточно рейтинга: %d. Необходимо больше %d.", rating, CUSTOM_TAG_MIN_RATING), true, 30)
		return
	}

	if err := userSetCustomTag(ctx, userID, tag); err != nil {
		zap.S().Infof("[setTagHandler] userSetCustomTag failed for userID=%d: %v", userID, err)
		systemAnswerToMessage(ctx, b, chatID, messageID, "Не удалось сохранить тег, попробуйте позже.", true, 30)
		return
	}

	answer := fmt.Sprintf("Тег «%s» установлен.", tag)
	if tag == "" {
		// Cleared: restore the automatic frag tag right away.
		if user, err := getUser(ctx, userID); err == nil {
			tag = fmt.Sprintf("frags: %d", user.VoteCounter)
		}
		answer = "Тег сброшен."
	}
	_, err = b.SetChatMemberTag(ctx, &bot.SetChatMemberTagParams{
		ChatID: chatID,
		UserID: userID,
		Tag:    tag,
	})
	if err != nil {
		zap.S().Infof("[setTagHandler] SetChatMemberTag failed: userID=%d chatID=%d: %v", userID, chatID, err)
	}
	systemAnswerToMessage(ctx, b, chatID, messageID, answer, true, 30)
}
