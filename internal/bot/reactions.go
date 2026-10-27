package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/ice2heart/poke_bot/internal/storage"
	"github.com/ice2heart/poke_bot/internal/tgutil"
	"go.uber.org/zap"
)

// deleteAllReactionsParams builds the deleteAllMessageReactions request.
// Negative IDs are channels/sender chats and go into ActorChatID, everything
// else is a regular user.
func deleteAllReactionsParams(chatID, userID int64) *bot.DeleteAllMessageReactionsParams {
	params := &bot.DeleteAllMessageReactionsParams{ChatID: chatID}
	if userID < 0 {
		params.ActorChatID = userID
	} else {
		params.UserID = userID
	}
	return params
}

// clearUserReactions removes up to 10000 recent reactions that the user (or
// sender chat) left in the chat. The bot needs can_delete_messages.
func clearUserReactions(ctx context.Context, b *bot.Bot, chatID, userID int64) (bool, error) {
	return b.DeleteAllMessageReactions(ctx, deleteAllReactionsParams(chatID, userID))
}

// parseReactionTarget interprets the /clear_reactions argument as either a
// numeric user ID or an @username. Anything else yields neither.
func parseReactionTarget(arg string) (userID int64, username string) {
	arg = strings.TrimSpace(arg)
	if strings.HasPrefix(arg, "@") && len(arg) > 1 {
		return 0, arg[1:]
	}
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil && id != 0 {
		return id, ""
	}
	return 0, ""
}

// resolveReactionTarget finds the user whose reactions should be cleared: the
// @username / numeric ID given as an argument, a text mention, or the author of
// the replied-to message. An explicit argument wins over the reply. It returns
// 0 when no target could be determined.
func (a *App) resolveReactionTarget(ctx context.Context, msg *models.Message) int64 {
	if fields := strings.Fields(msg.Text); len(fields) >= 2 {
		userID, username := parseReactionTarget(fields[1])
		if userID != 0 {
			return userID
		}
		if username != "" {
			return a.resolveUsername(ctx, username)
		}
	}
	for _, e := range msg.Entities {
		if e.Type == models.MessageEntityTypeTextMention && e.User != nil {
			return e.User.ID
		}
	}
	// In forum topics every message "replies" to the topic-created service
	// message, which is not a target.
	if r := msg.ReplyToMessage; r != nil && r.ForumTopicCreated == nil {
		if r.SenderChat != nil {
			return r.SenderChat.ID
		}
		if r.From != nil {
			return r.From.ID
		}
	}
	return 0
}

// resolveUsername looks username up in the database, then via MTProto.
func (a *App) resolveUsername(ctx context.Context, username string) int64 {
	if user, err := storage.GetRatingFromUsername(ctx, username); err == nil {
		return user.Userid
	}
	userInfo, err := a.client.GetUserByUsername(ctx, username)
	if err != nil {
		zap.S().Infof("[clearReactionsHandler] GetUserByUsername failed for username=%q: %v", username, err)
		return 0
	}
	return userInfo.UserId
}

// clearReactionsHandler removes all recent reactions of a user from the chat.
// Admin only.
func (a *App) clearReactionsHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	msg := update.Message
	chatID := msg.Chat.ID
	if msg.From == nil || !a.isUserAdmin(ctx, b, chatID, msg.From.ID, chatID, msg.ID, ephemeralFor(msg)) {
		return
	}

	userID := a.resolveReactionTarget(ctx, msg)
	if userID == 0 {
		systemAnswerToMessage(ctx, b, chatID, msg.ID,
			tgutil.Escape("Ответьте на сообщение пользователя или укажите его.\nПримеры:\n/clear_reactions @username\n/clear_reactions 123456789"),
			true, 30, ephemeralFor(msg))
		return
	}

	zap.S().Infof("[clearReactionsHandler] adminID=%d clears reactions of userID=%d in chatID=%d", msg.From.ID, userID, chatID)
	a.reactionCache.Delete(reactionKey{chatID: chatID, userID: userID})
	ok, err := clearUserReactions(ctx, b, chatID, userID)
	text := fmt.Sprintf("Реакции пользователя %d удалены", userID)
	if err != nil || !ok {
		zap.S().Infof("[clearReactionsHandler] clearUserReactions failed: userID=%d chatID=%d: %v", userID, chatID, err)
		text = "Не удалось удалить реакции"
	}
	systemAnswerToMessage(ctx, b, chatID, msg.ID, tgutil.Escape(text), true, 30, ephemeralFor(msg))
}
