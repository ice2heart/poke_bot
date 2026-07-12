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

// CHECK_LAST_MESSAGES is how many recent messages /check shows.
const CHECK_LAST_MESSAGES = 5

// formatCheckReport builds the MarkdownV2 summary for /check: the user's
// rating breakdown and their most recent stored messages (newest first).
func formatCheckReport(user *UserRecord, messages []ChatMessage) string {
	rating := int(user.Counter + user.VoteCounter*VOTE_RATING_MULTIPLY)
	lines := []string{
		user.toClickableUsername(),
		fmt.Sprintf("Рейтинг: %d \\(сообщений: %d, фрагов: %d\\)", rating, user.Counter, user.VoteCounter),
	}
	if user.MuteCounter > 0 {
		lines = append(lines, fmt.Sprintf("Мутов: %d", user.MuteCounter))
	}
	if len(messages) > 0 {
		lines = append(lines, "Последние сообщения:")
		for _, m := range messages {
			lines = append(lines, quoteText(firstN(m.Text, 200)))
		}
	}
	return firstN(strings.Join(lines, "\n"), 3500)
}

// checkHandler serves the public /check command: shows a user's rating and
// last messages. Target is given as @username or a text mention; without a
// target it reports on the sender.
func checkHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	messageID := update.Message.ID

	var user *UserRecord
	var err error
	for _, v := range update.Message.Entities {
		switch v.Type {
		case models.MessageEntityTypeTextMention:
			user, err = getUser(ctx, v.User.ID)
		case models.MessageEntityTypeMention:
			username := entityText(update.Message.Text, v.Offset+1, v.Length-1)
			if username == myID {
				continue
			}
			user, err = getUserByUsername(ctx, username)
		default:
			continue
		}
		break
	}
	if user == nil && err == nil {
		// No target given — report on the sender.
		userID := update.Message.From.ID
		if update.Message.SenderChat != nil {
			userID = update.Message.SenderChat.ID
		}
		user, err = getUser(ctx, userID)
	}
	if err != nil || user == nil {
		systemAnswerToMessage(ctx, b, chatID, messageID, "Пользователь не найден", true, 30)
		return
	}

	messages, err := getUserLastNthMessages(ctx, user.Uid, chatID, CHECK_LAST_MESSAGES)
	if err != nil {
		zap.S().Infof("[checkHandler] getUserLastNthMessages failed for userID=%d chatID=%d: %v", user.Uid, chatID, err)
	}
	systemAnswerToMessage(ctx, b, chatID, messageID, formatCheckReport(user, messages), true, 60)
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
