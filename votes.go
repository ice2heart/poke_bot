package main

import (
	"context"
	"fmt"
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

	sessionsMux.Lock()
	defer sessionsMux.Unlock()

	s, chatSession, superPoke, ok := parseVoteSession(ctx, b, update)
	if !ok {
		return
	}

	if s.OwnerID == update.CallbackQuery.From.ID && superPoke == 0 {
		zap.S().Infof("[voteCallbackHandler] userID=%d attempted to vote on their own poll in chatID=%d", update.CallbackQuery.From.ID, s.ChatID)
		answer = ANSWER_OWN
		return
	}

	answer = handleVote(ctx, b, update, s, chatSession, superPoke)
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

// handleVote records the vote, tallies the result, and dispatches the outcome.
// Returns the answer message to send back to the voter.
// Caller must hold sessionsMux.
func handleVote(ctx context.Context, b *bot.Bot, update *models.Update, s *BanInfo, chatSession map[int64]*BanInfo, superPoke int) string {
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
		if s.cancelPin != nil {
			s.cancelPin()
		}
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
		delete(chatSession, msgID)
		return answer
	case -1:
		if s.cancelPin != nil {
			s.cancelPin()
		}
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: s.ChatID, MessageID: int(s.VoteMessageID)})
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: s.ChatID, MessageID: int(s.RequestMessageID)})
		delete(chatSession, msgID)
		return answer
	}

	b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
		MessageID:   update.CallbackQuery.Message.Message.ID,
		ReplyMarkup: getVoteButtons(upvotes, downvotes, s.Type),
	})

	return answer
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

// voteExpiredText builds the MarkdownV2 announcement for an expired vote,
// mentioning the user by @username or by a tg://user link when absent.
func voteExpiredText(userName, profileName string, userID int64) string {
	var username string
	if userName == "" {
		username = fmt.Sprintf("[%s](tg://user?id=%d)", strings.TrimSpace(escape(profileName)), userID)
	} else {
		username = fmt.Sprintf("@%s", escape(userName))
	}
	return fmt.Sprintf("Голосование истекло — необходимое количество голосов не набрано\\. %s", username)
}

// expireVoteLocked removes one active vote: cancels the pending pin, deletes
// the vote and request messages, and announces the expiry in the chat.
// Caller must hold sessionsMux and delete the session entry afterwards.
func expireVoteLocked(ctx context.Context, chatID int64, msgID int64, s *BanInfo) {
	if s.cancelPin != nil {
		s.cancelPin()
	}
	myBot.UnpinChatMessage(ctx, &bot.UnpinChatMessageParams{
		ChatID:    chatID,
		MessageID: int(msgID),
	})
	myBot.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    chatID,
		MessageID: int(msgID),
	})
	myBot.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    chatID,
		MessageID: int(s.RequestMessageID),
	})
	myBot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      voteExpiredText(s.UserName, s.ProfileName, s.UserID),
		ParseMode: models.ParseModeMarkdown,
	})
}

func expireOldVotes(ctx context.Context) {
	const maxAge = 36 * time.Hour // 1.5 days

	sessionsMux.Lock()
	defer sessionsMux.Unlock()

	for chatID, chatSession := range sessions {
		for msgID, s := range chatSession {
			if time.Since(s.CreatedAt) < maxAge {
				continue
			}
			zap.S().Infof("[expireOldVotes] expiring vote messageID=%d chatID=%d userID=%d", msgID, chatID, s.UserID)
			expireVoteLocked(ctx, chatID, msgID, s)
			delete(chatSession, msgID)
		}
		if len(chatSession) == 0 {
			delete(sessions, chatID)
		}
	}
}

func expireAllVotes(ctx context.Context) {
	sessionsMux.Lock()
	defer sessionsMux.Unlock()

	for chatID, chatSession := range sessions {
		for msgID, s := range chatSession {
			zap.S().Infof("[expireAllVotes] expiring vote messageID=%d chatID=%d userID=%d", msgID, chatID, s.UserID)
			expireVoteLocked(ctx, chatID, msgID, s)
			delete(chatSession, msgID)
		}
		if len(chatSession) == 0 {
			delete(sessions, chatID)
		}
	}
}
