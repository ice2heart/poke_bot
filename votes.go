package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

var (
	sessionsMux sync.Mutex
	sessions    map[int64]map[int64]*BanInfo = make(map[int64]map[int64]*BanInfo)

	ANSWER_OWN             string = "Нельзя голосовать в собственном голосовании"
	ANSWER_NOTBAN          string = "Голос против бана принят"
	ANSWER_BAN             string = "Голос за бан принят"
	ANSWER_MUTE            string = "Голос за мут принят"
	ANSWER_NOTMUTE         string = "Голос против мута принят"
	ANSWER_SOMETHING_WRONG string = "Произошла ошибка. Попробуйте позже"
)

func checkForDuplicates(ctx context.Context, chatId int64, userid int64, b *bot.Bot, update *models.Update) bool {
	// Check for duplicates
	sessionsMux.Lock()
	defer sessionsMux.Unlock()

	chatSessions, ok := sessions[chatId]
	if !ok {
		sessions[chatId] = map[int64]*BanInfo{}
		chatSessions = sessions[chatId]
	}

	for responseMessage, messageSession := range chatSessions {
		if messageSession.UserID != userid {
			continue
		}
		systemAnswerToMessage(ctx, b, chatId, update.Message.ID, fmt.Sprintf("[Голосование уже создано](tg://privatepost?channel=%s&post=%d)", makePublicGroupString(chatId), responseMessage), true, 30)
		return true
	}

	return false
}

func makeVoteMessage(ctx context.Context, banInfo *BanInfo, b *bot.Bot) bool {
	// голосуем за бан @пользователя необходимо Н голосов
	//  Последнее сообщение: тут текст

	responseMessage, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    banInfo.ChatID,
		Text:      banInfo.BanMessage,
		ParseMode: models.ParseModeMarkdown,
		ReplyParameters: &models.ReplyParameters{
			ChatID:    banInfo.ChatID,
			MessageID: int(banInfo.RequestMessageID),
		},
		ReplyMarkup: getVoteButtons(0, 0, banInfo.Type),
		LinkPreviewOptions: &models.LinkPreviewOptions{
			IsDisabled: bot.True(),
		},
	})
	if err != nil {
		zap.S().Infof("[makeVoteMessage] SendMessage failed: userID=%d chatID=%d type=%d: %v", banInfo.UserID, banInfo.ChatID, banInfo.Type, err)
		return false
	}
	zap.S().Infof("[makeVoteMessage] vote message sent: messageID=%d chatID=%d userID=%d", responseMessage.ID, banInfo.ChatID, banInfo.UserID)

	sessionsMux.Lock()
	defer sessionsMux.Unlock()

	chatSessions, ok := sessions[banInfo.ChatID]
	if !ok {
		sessions[banInfo.ChatID] = map[int64]*BanInfo{}
		chatSessions = sessions[banInfo.ChatID]
	}
	banInfo.Voters = map[int64]int8{}
	banInfo.VoteMessageID = int64(responseMessage.ID)
	banInfo.CreatedAt = time.Now()
	chatSessions[int64(responseMessage.ID)] = banInfo

	pinChatID := banInfo.ChatID
	pinMessageID := responseMessage.ID
	pinCtx, cancelPin := context.WithCancel(ctx)
	banInfo.cancelPin = cancelPin
	go delay(pinCtx, 5*60, func() {
		b.PinChatMessage(ctx, &bot.PinChatMessageParams{
			ChatID:              pinChatID,
			MessageID:           pinMessageID,
			DisableNotification: true,
		})
	})

	return true
}

func onPauseMessage(ctx context.Context, b *bot.Bot, message *models.Message) {
	systemAnswerToMessage(ctx, b, message.Chat.ID, message.ID, fmt.Sprintf("[%s %s](tg://user?id=%d), бот в данный момент приостановлен", message.From.FirstName, message.From.LastName, message.From.ID), true, 30)
}

func voteCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	answer := ANSWER_SOMETHING_WRONG
	defer func() {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
			ShowAlert:       false,
			Text:            answer,
		})
	}()

	if update.CallbackQuery == nil || update.CallbackQuery.Message.Message == nil {
		return
	}

	var action func()

	sessionsMux.Lock()
	s, chatSession, superPoke, ok := parseVoteSession(ctx, b, update)
	if !ok {
		sessionsMux.Unlock()
		return
	}

	if s.OwnerID == update.CallbackQuery.From.ID && superPoke == 0 {
		zap.S().Infof("[voteCallbackHandler] userID=%d attempted to vote on their own poll in chatID=%d", update.CallbackQuery.From.ID, s.ChatID)
		sessionsMux.Unlock()
		answer = ANSWER_OWN
		return
	}

	answer, action = handleVote(ctx, b, update, s, chatSession, superPoke)
	sessionsMux.Unlock()

	// action performs the network I/O for a decided vote (ban/mute/expire).
	// It is run after releasing sessionsMux so slow Telegram round-trips do
	// not block voting in other chats, and to avoid holding sessionsMux while
	// the moderation actions acquire settingsMux.
	if action != nil {
		action()
	}
}

// parseVoteSession looks up the active BanInfo session for the incoming callback.
// Caller must hold sessionsMux.
func parseVoteSession(ctx context.Context, b *bot.Bot, update *models.Update) (s *BanInfo, chatSession map[int64]*BanInfo, superPoke int, ok bool) {
	msg := update.CallbackQuery.Message.Message
	zap.S().Infof("[voteCallbackHandler] vote=%q messageID=%d chatID=%d userID=%d",
		update.CallbackQuery.Data, msg.ID, msg.Chat.ID, update.CallbackQuery.From.ID)

	chatSession, ok = sessions[msg.Chat.ID]
	if !ok {
		zap.S().Infof("[voteCallbackHandler] no active session for chatID=%d", msg.Chat.ID)
		return nil, nil, 0, false
	}
	s, ok = chatSession[int64(msg.ID)]
	if !ok {
		zap.S().Infof("[voteCallbackHandler] no session for messageID=%d in chatID=%d, deleting stale vote message", msg.ID, msg.Chat.ID)
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: msg.Chat.ID, MessageID: msg.ID})
		return nil, nil, 0, false
	}

	adminsMux.Lock()
	isAdmin, isInAdminList := checkAdmins(ctx, b, s.ChatID)[update.CallbackQuery.From.ID]
	if isInAdminList && isAdmin {
		superPoke = 1
		if update.CallbackQuery.Data == "button_downvote" {
			superPoke = -1
		}
	}
	adminsMux.Unlock()

	// The vote owner cancelling their own vote counts as a super downvote.
	if superPoke == 0 && s.OwnerID == update.CallbackQuery.From.ID && update.CallbackQuery.Data == "button_downvote" {
		superPoke = -1
	}

	return s, chatSession, superPoke, true
}

// handleVote records the vote and tallies the result. It performs the session
// state mutations that require sessionsMux (recording the vote, deleting a
// decided session) and returns the answer for the voter plus an optional
// action closure holding the network I/O for a decided vote.
//
// The caller must hold sessionsMux while calling handleVote, then release it
// and run the returned action (if non-nil) so slow Telegram round-trips and the
// moderation actions' settingsMux usage do not happen under sessionsMux.
func handleVote(ctx context.Context, b *bot.Bot, update *models.Update, s *BanInfo, chatSession map[int64]*BanInfo, superPoke int) (string, func()) {
	isDownvote := update.CallbackQuery.Data == "button_downvote"
	zap.S().Infof("[handleVote] userID=%d chatID=%d type=%d superPoke=%d isDownvote=%v voters=%d score=%d",
		update.CallbackQuery.From.ID, s.ChatID, s.Type, superPoke, isDownvote, len(s.Voters), s.Score)

	voteResult := 1
	answer := ANSWER_BAN
	if s.Type != BAN {
		answer = ANSWER_MUTE
	}
	if isDownvote {
		voteResult = -1
		answer = ANSWER_NOTBAN
		if s.Type != BAN {
			answer = ANSWER_NOTMUTE
		}
	}

	s.Voters[update.CallbackQuery.From.ID] = int8(voteResult)

	upvotes, downvotes := tallyVotes(s.Voters)

	msgID := int64(update.CallbackQuery.Message.Message.ID)

	switch voteVerdict(upvotes, downvotes, superPoke, s.Score) {
	case 1:
		// Claim the session under the lock: deleting it here guarantees the
		// moderation action below runs exactly once even if concurrent
		// callbacks arrive for the same vote message.
		if s.cancelPin != nil {
			s.cancelPin()
		}
		delete(chatSession, msgID)
		return answer, func() {
			var actionResult bool
			switch s.Type {
			case BAN:
				actionResult = banUser(ctx, b, s)
			case MUTE:
				actionResult = muteUser(ctx, b, s)
			case TEXT_ONLY:
				actionResult = textOnlyUser(ctx, b, s)
			}
			if actionResult {
				go updateUserFragTag(ctx, b, s.ChatID, s.OwnerID)
			}
		}
	case -1:
		if s.cancelPin != nil {
			s.cancelPin()
		}
		delete(chatSession, msgID)
		return answer, func() {
			b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: s.ChatID, MessageID: int(s.VoteMessageID)})
			b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: s.ChatID, MessageID: int(s.RequestMessageID)})
		}
	}

	return answer, func() {
		b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
			ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
			MessageID:   update.CallbackQuery.Message.Message.ID,
			ReplyMarkup: getVoteButtons(upvotes, downvotes, s.Type),
		})
	}
}

// tallyVotes counts the up and down votes recorded in a session's voter map.
func tallyVotes(voters map[int64]int8) (upvotes, downvotes int) {
	for _, v := range voters {
		if v == 1 {
			upvotes++
		} else {
			downvotes++
		}
	}
	return upvotes, downvotes
}

// voteVerdict decides a vote's outcome: 1 applies the action, -1 cancels the
// vote, 0 keeps collecting votes. An admin superPoke overrides the tallies.
func voteVerdict(upvotes, downvotes, superPoke int, score int16) int {
	if superPoke == 1 || upvotes-downvotes >= int(score) {
		return 1
	}
	if superPoke == -1 || downvotes-upvotes >= int(MID_SCORE) {
		return -1
	}
	return 0
}

// userTag formats a clickable MarkdownV2 mention: @username when known,
// otherwise the profile name as a tg://user link.
func userTag(userName, profileName string, userID int64) string {
	if userName == "" {
		return fmt.Sprintf("[%s](tg://user?id=%d)", strings.TrimSpace(escape(profileName)), userID)
	}
	return fmt.Sprintf("@%s", escape(userName))
}

// voteExpiredText builds the MarkdownV2 announcement for an expired vote,
// mentioning the user by @username or by a tg://user link when absent.
func voteExpiredText(userName, profileName string, userID int64) string {
	return fmt.Sprintf("Голосование истекло — необходимое количество голосов не набрано\\. %s", userTag(userName, profileName, userID))
}

// formatVotersReport builds the MarkdownV2 breakdown of a finished vote:
// who voted for and against, in deterministic (ascending user ID) order.
// tagFor resolves a voter ID to a clickable mention.
func formatVotersReport(banInfo *BanInfo, tagFor func(int64) string) string {
	lines := []string{fmt.Sprintf("Голосование по %s", userTag(banInfo.UserName, banInfo.ProfileName, banInfo.UserID))}

	voterIDs := make([]int64, 0, len(banInfo.Voters))
	for id := range banInfo.Voters {
		voterIDs = append(voterIDs, id)
	}
	slices.Sort(voterIDs)

	var upvoters, downvoters []string
	for _, id := range voterIDs {
		if banInfo.Voters[id] == 1 {
			upvoters = append(upvoters, tagFor(id))
		} else {
			downvoters = append(downvoters, tagFor(id))
		}
	}

	if len(upvoters) == 0 && len(downvoters) == 0 {
		lines = append(lines, "Голосов не зафиксировано")
		return strings.Join(lines, "\n")
	}
	if len(upvoters) > 0 {
		lines = append(lines, fmt.Sprintf("За \\(%d\\):", len(upvoters)))
		lines = append(lines, upvoters...)
	}
	if len(downvoters) > 0 {
		lines = append(lines, fmt.Sprintf("Против \\(%d\\):", len(downvoters)))
		lines = append(lines, downvoters...)
	}
	return strings.Join(lines, "\n")
}

// expiredVote pairs a removed session with its vote message ID so the network
// side of expiry can run after sessionsMux is released.
type expiredVote struct {
	chatID int64
	msgID  int64
	s      *BanInfo
}

// collectExpiredVotesLocked removes every session matching shouldExpire from the
// sessions map, cancels each pending pin, and returns the removed sessions for
// out-of-lock expiry. Caller must hold sessionsMux.
func collectExpiredVotesLocked(shouldExpire func(*BanInfo) bool, tag string) []expiredVote {
	var expired []expiredVote
	for chatID, chatSession := range sessions {
		for msgID, s := range chatSession {
			if !shouldExpire(s) {
				continue
			}
			zap.S().Infof("[%s] expiring vote messageID=%d chatID=%d userID=%d", tag, msgID, chatID, s.UserID)
			if s.cancelPin != nil {
				s.cancelPin()
			}
			delete(chatSession, msgID)
			expired = append(expired, expiredVote{chatID: chatID, msgID: msgID, s: s})
		}
		if len(chatSession) == 0 {
			delete(sessions, chatID)
		}
	}
	return expired
}

// expireVotes performs the network side of expiry for already-removed sessions:
// it unpins/deletes the vote and request messages and announces the expiry.
// Must be called without holding sessionsMux.
func expireVotes(ctx context.Context, expired []expiredVote) {
	for _, e := range expired {
		myBot.UnpinChatMessage(ctx, &bot.UnpinChatMessageParams{
			ChatID:    e.chatID,
			MessageID: int(e.msgID),
		})
		myBot.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    e.chatID,
			MessageID: int(e.msgID),
		})
		myBot.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    e.chatID,
			MessageID: int(e.s.RequestMessageID),
		})
		myBot.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    e.chatID,
			Text:      voteExpiredText(e.s.UserName, e.s.ProfileName, e.s.UserID),
			ParseMode: models.ParseModeMarkdown,
		})
	}
}

func expireOldVotes(ctx context.Context) {
	const maxAge = 36 * time.Hour // 1.5 days

	sessionsMux.Lock()
	expired := collectExpiredVotesLocked(func(s *BanInfo) bool {
		return time.Since(s.CreatedAt) >= maxAge
	}, "expireOldVotes")
	sessionsMux.Unlock()

	expireVotes(ctx, expired)
}

func expireAllVotes(ctx context.Context) {
	sessionsMux.Lock()
	expired := collectExpiredVotesLocked(func(*BanInfo) bool { return true }, "expireAllVotes")
	sessionsMux.Unlock()

	expireVotes(ctx, expired)
}
