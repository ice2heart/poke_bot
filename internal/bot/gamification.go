package bot

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/ice2heart/poke_bot/internal/storage"
	"github.com/ice2heart/poke_bot/internal/tgutil"
	"go.uber.org/zap"
)

func (a *App) bestHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID

	if !a.isUserAdmin(ctx, b, chatID, userID, chatID, update.Message.ID, ephemeralFor(update.Message)) {
		return
	}

	a.adminsMux.Lock()
	chatAdmins := a.checkAdmins(ctx, b, chatID)
	a.adminsMux.Unlock()

	topUsers, err := storage.GetTopUsersByVotes(ctx, 100)
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
		lines = append(lines, fmt.Sprintf("%d\\. %s : %d", rank, toClickableUsername(&user), user.VoteCounter))
		rank++
		if rank > 20 {
			break
		}
	}

	deleteCommand(ctx, b, update.Message)

	if len(lines) == 0 {
		systemMessage(ctx, b, chatID, "Нет данных", 30, ephemeralFor(update.Message))
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
func formatCheckReport(user *storage.UserRecord, messages []storage.ChatMessage) string {
	rating := int(user.Counter + user.VoteCounter*storage.VOTE_RATING_MULTIPLY)
	lines := []string{
		toClickableUsername(user),
		fmt.Sprintf("Рейтинг: %d \\(сообщений: %d, фрагов: %d\\)", rating, user.Counter, user.VoteCounter),
	}
	if user.MuteCounter > 0 {
		lines = append(lines, fmt.Sprintf("Мутов: %d", user.MuteCounter))
	}
	if len(messages) > 0 {
		lines = append(lines, "Последние сообщения:")
		for _, m := range messages {
			lines = append(lines, tgutil.QuoteText(tgutil.FirstN(m.Text, 200)))
		}
	}
	return tgutil.FirstN(strings.Join(lines, "\n"), 3500)
}

// checkHandler serves the public /check command: shows a user's rating and
// last messages. Target is given as @username or a text mention; without a
// target it reports on the sender.
func (a *App) checkHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	messageID := update.Message.ID

	var user *storage.UserRecord
	var err error
	for _, v := range update.Message.Entities {
		switch v.Type {
		case models.MessageEntityTypeTextMention:
			user, err = storage.GetUser(ctx, v.User.ID)
		case models.MessageEntityTypeMention:
			username := tgutil.EntityText(update.Message.Text, v.Offset+1, v.Length-1)
			if username == a.myID {
				continue
			}
			user, err = storage.GetUserByUsername(ctx, username)
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
		user, err = storage.GetUser(ctx, userID)
	}
	if err != nil || user == nil {
		systemAnswerToMessage(ctx, b, chatID, messageID, "Пользователь не найден", true, 30, ephemeralFor(update.Message))
		return
	}

	messages, err := storage.GetUserLastNthMessages(ctx, user.Uid, chatID, CHECK_LAST_MESSAGES)
	if err != nil {
		zap.S().Infof("[checkHandler] getUserLastNthMessages failed for userID=%d chatID=%d: %v", user.Uid, chatID, err)
	}
	systemAnswerToMessage(ctx, b, chatID, messageID, formatCheckReport(user, messages), true, 60, ephemeralFor(update.Message))
}

const CUSTOM_TAG_MAX_LENGTH = 16

func setTagHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	messageID := update.Message.ID

	if update.Message.SenderChat != nil {
		systemAnswerToMessage(ctx, b, chatID, messageID, tgutil.Escape("Команда недоступна для каналов."), true, 30, ephemeralFor(update.Message))
		return
	}
	userID := update.Message.From.ID

	_, tag, _ := strings.Cut(update.Message.Text, " ")
	tag = strings.TrimSpace(tag)
	if utf8.RuneCountInString(tag) > CUSTOM_TAG_MAX_LENGTH {
		systemAnswerToMessage(ctx, b, chatID, messageID, tgutil.Escape(fmt.Sprintf("Слишком длинный тег, максимум %d символов.", CUSTOM_TAG_MAX_LENGTH)), true, 30, ephemeralFor(update.Message))
		return
	}

	score, err := storage.GetRatingFromUserID(ctx, userID)
	if err != nil || score.Rating <= storage.CUSTOM_TAG_MIN_RATING {
		rating := 0
		if score != nil {
			rating = score.Rating
		}
		systemAnswerToMessage(ctx, b, chatID, messageID, tgutil.Escape(fmt.Sprintf("Недостаточно рейтинга: %d. Необходимо больше %d.", rating, storage.CUSTOM_TAG_MIN_RATING)), true, 30, ephemeralFor(update.Message))
		return
	}

	if err := storage.UserSetCustomTag(ctx, userID, tag); err != nil {
		zap.S().Infof("[setTagHandler] userSetCustomTag failed for userID=%d: %v", userID, err)
		systemAnswerToMessage(ctx, b, chatID, messageID, tgutil.Escape("Не удалось сохранить тег, попробуйте позже."), true, 30, ephemeralFor(update.Message))
		return
	}

	answer := tgutil.Escape(fmt.Sprintf("Тег «%s» установлен.", tag))
	if tag == "" {
		// Cleared: restore the automatic frag tag right away.
		if user, err := storage.GetUser(ctx, userID); err == nil {
			tag = fmt.Sprintf("frags: %d", user.VoteCounter)
		}
		answer = tgutil.Escape("Тег сброшен.")
	}
	_, err = b.SetChatMemberTag(ctx, &bot.SetChatMemberTagParams{
		ChatID: chatID,
		UserID: userID,
		Tag:    tag,
	})
	if err != nil {
		zap.S().Infof("[setTagHandler] SetChatMemberTag failed: userID=%d chatID=%d: %v", userID, chatID, err)
	}
	systemAnswerToMessage(ctx, b, chatID, messageID, answer, true, 30, ephemeralFor(update.Message))
}
