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

// Callback data carried by the two vote buttons.
const (
	BUTTON_UPVOTE   string = "button_upvote"
	BUTTON_DOWNVOTE string = "button_downvote"
)

// The direction of a cast vote, as stored in BanInfo.Voters.
const (
	VOTE_UP   int8 = 1
	VOTE_DOWN int8 = -1
)

var (
	sessionsMux sync.Mutex
	sessions    map[int64]map[int64]*BanInfo = make(map[int64]map[int64]*BanInfo)

	ANSWER_OWN             string = "Нельзя голосовать в собственном голосовании"
	ANSWER_COUNTED         string = "Ваш голос уже учтён"
	ANSWER_SOMETHING_WRONG string = "Произошла ошибка. Попробуйте позже"
)

// findSessionByUser returns the running vote against userid in the chat, and the
// vote message it is keyed by. Caller must hold sessionsMux.
func findSessionByUser(chatId int64, userid int64) (*BanInfo, map[int64]*BanInfo, int64) {
	chatSessions, ok := sessions[chatId]
	if !ok {
		sessions[chatId] = map[int64]*BanInfo{}
		chatSessions = sessions[chatId]
	}
	for msgID, s := range chatSessions {
		if s.UserID == userid {
			return s, chatSessions, msgID
		}
	}
	return nil, chatSessions, 0
}

// checkForDuplicates reports whether a vote against userid is already running in
// the chat. A repeated command is not rejected outright: it counts as an upvote
// from voterID on the existing vote, which may decide it right away. When the
// vote is still running afterwards, the caller is answered with a link to it.
func checkForDuplicates(ctx context.Context, chatId int64, userid int64, voterID int64, b *bot.Bot, update *models.Update) bool {
	sessionsMux.Lock()

	s, chatSessions, msgID := findSessionByUser(chatId, userid)
	if s == nil {
		sessionsMux.Unlock()
		return false
	}

	// A repeated command is an upvote from the requester, but it must never flip
	// or double-count a vote they already cast on the button.
	result := castVote(ctx, b, s, chatSessions, msgID, voterID, VOTE_UP, 0, false)
	sessionsMux.Unlock()

	if result.action != nil {
		result.action()
	}

	if result.decided {
		// The vote is over and its message is gone: nothing left to point at.
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatId, MessageID: update.Message.ID})
		return true
	}

	text := fmt.Sprintf("[Голосование уже создано](tg://privatepost?channel=%s&post=%d)",
		makePublicGroupString(chatId), msgID)
	if result.counted {
		text = fmt.Sprintf("%s\n%s", escape("Ваш голос +1 добавлен к голосованию"), text)
	} else if result.answer != "" {
		text = fmt.Sprintf("%s\n%s", escape(result.answer), text)
	}
	systemAnswerToMessage(ctx, b, chatId, update.Message.ID, text, true, 30)
	return true
}

func makeVoteMessage(ctx context.Context, banInfo *BanInfo, b *bot.Bot) bool {
	// голосуем за бан @пользователя необходимо Н голосов
	//  Последнее сообщение: тут текст

	params := &bot.SendMessageParams{
		ChatID:      banInfo.ChatID,
		Text:        banInfo.BanMessage,
		ParseMode:   models.ParseModeMarkdown,
		ReplyMarkup: getVoteButtons(0, 0, banInfo.Type),
		LinkPreviewOptions: &models.LinkPreviewOptions{
			IsDisabled: bot.True(),
		},
	}
	// Automatic votes have no request message to reply to.
	if banInfo.RequestMessageID != 0 {
		params.ReplyParameters = &models.ReplyParameters{
			ChatID:    banInfo.ChatID,
			MessageID: int(banInfo.RequestMessageID),
		}
	}
	responseMessage, err := b.SendMessage(ctx, params)
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
	s, chatSession, superPoke, ok := parseVoteSession(ctx, b, update)
	if !ok {
		sessionsMux.Unlock()
		return
	}

	direction := VOTE_UP
	if update.CallbackQuery.Data == BUTTON_DOWNVOTE {
		direction = VOTE_DOWN
	}

	// Pressing a button lets a voter change a vote they already cast.
	result := castVote(ctx, b, s, chatSession, int64(update.CallbackQuery.Message.Message.ID),
		update.CallbackQuery.From.ID, direction, superPoke, true)
	sessionsMux.Unlock()

	answer = result.answer

	// action performs the network I/O for a decided vote (ban/mute/expire).
	// It is run after releasing sessionsMux so slow Telegram round-trips do
	// not block voting in other chats, and to avoid holding sessionsMux while
	// the moderation actions acquire settingsMux.
	if result.action != nil {
		result.action()
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
		if update.CallbackQuery.Data == BUTTON_DOWNVOTE {
			superPoke = -1
		}
	}
	adminsMux.Unlock()

	// The vote owner cancelling their own vote counts as a super downvote.
	if superPoke == 0 && s.OwnerID == update.CallbackQuery.From.ID && update.CallbackQuery.Data == BUTTON_DOWNVOTE {
		superPoke = -1
	}

	return s, chatSession, superPoke, true
}

// voteResult is the outcome of casting a vote: the answer to show the voter,
// whether the vote was actually recorded, whether the vote settled, and the
// network I/O the caller must run once it has released sessionsMux.
type voteResult struct {
	answer  string
	counted bool
	decided bool
	action  func()
}

// castVote records voterID's vote of the given direction on s, tallies the
// session, and settles it if the tally (or an admin superPoke) decides it. It is
// the single entry point for adding a vote, whether it arrived by pressing a
// button or by repeating the command that started the vote.
//
// replace tells whether an existing vote by voterID may be overwritten: pressing
// a button lets a voter change their mind, repeating a command must not silently
// flip or double-count the vote they already cast.
//
// The caller must hold sessionsMux across the call — castVote both reads and
// mutates the session — and must run result.action only after releasing it, so
// that slow Telegram round-trips and the moderation actions' own settingsMux use
// never happen under sessionsMux.
func castVote(ctx context.Context, b *bot.Bot, s *BanInfo, chatSession map[int64]*BanInfo, msgID int64, voterID int64, direction int8, superPoke int, replace bool) voteResult {
	vt, ok := voteTypes[s.Type]
	if !ok {
		zap.S().Infof("[castVote] unknown vote type %d for chatID=%d messageID=%d", s.Type, s.ChatID, msgID)
		return voteResult{answer: ANSWER_SOMETHING_WRONG}
	}

	// The vote's owner already counts as being for it, and cannot vote again.
	if s.OwnerID == voterID && superPoke == 0 {
		return voteResult{answer: ANSWER_OWN}
	}
	if _, voted := s.Voters[voterID]; voted && !replace {
		return voteResult{answer: ANSWER_COUNTED}
	}

	zap.S().Infof("[castVote] voterID=%d chatID=%d messageID=%d type=%d direction=%d superPoke=%d voters=%d score=%d",
		voterID, s.ChatID, msgID, s.Type, direction, superPoke, len(s.Voters), s.Score)

	s.Voters[voterID] = direction
	upvotes, downvotes := tallyVotes(s.Voters)

	answer := vt.upAnswer
	if direction == VOTE_DOWN {
		answer = vt.downAnswer
	}

	switch voteVerdict(upvotes, downvotes, superPoke, s.Score) {
	case 1:
		// Claim the session under the lock: deleting it here guarantees the
		// moderation action below runs exactly once even if concurrent votes
		// arrive for the same vote message.
		settleSession(s, chatSession, msgID)
		return voteResult{answer: answer, counted: true, decided: true, action: func() {
			if vt.apply(ctx, b, s) {
				go updateUserFragTag(ctx, b, s.ChatID, s.OwnerID)
			}
		}}
	case -1:
		settleSession(s, chatSession, msgID)
		return voteResult{answer: answer, counted: true, decided: true, action: func() {
			b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: s.ChatID, MessageID: int(s.VoteMessageID)})
			if s.RequestMessageID != 0 {
				b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: s.ChatID, MessageID: int(s.RequestMessageID)})
			}
		}}
	}

	// Still collecting votes: just refresh the counts on the buttons.
	return voteResult{answer: answer, counted: true, action: func() {
		b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
			ChatID:      s.ChatID,
			MessageID:   int(msgID),
			ReplyMarkup: getVoteButtons(upvotes, downvotes, s.Type),
		})
	}}
}

// settleSession removes a decided vote from the chat's sessions and cancels its
// pending pin. Caller must hold sessionsMux.
func settleSession(s *BanInfo, chatSession map[int64]*BanInfo, msgID int64) {
	if s.cancelPin != nil {
		s.cancelPin()
	}
	delete(chatSession, msgID)
}

// tallyVotes counts the up and down votes recorded in a session's voter map.
func tallyVotes(voters map[int64]int8) (upvotes, downvotes int) {
	for _, v := range voters {
		if v == VOTE_UP {
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
		if e.s.RequestMessageID != 0 {
			myBot.DeleteMessage(ctx, &bot.DeleteMessageParams{
				ChatID:    e.chatID,
				MessageID: int(e.s.RequestMessageID),
			})
		}
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
