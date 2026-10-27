package bot

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot"
	"github.com/ice2heart/poke_bot/internal/storage"
	"github.com/ice2heart/poke_bot/internal/tgutil"
	"go.uber.org/zap"
)

const (
	BAN uint8 = iota
	MUTE
	TEXT_ONLY
)

// voteType collects everything that varies between the kinds of vote: the vote
// buttons, the answers shown to a voter, and the moderation action applied when
// the vote passes. Adding a kind of vote means adding one entry to voteTypes —
// the voting machinery itself is type-agnostic.
type voteType struct {
	upText   string // upvote button label; the vote count is appended
	downText string // downvote button label; the vote count is appended

	upAnswer   string // shown to a voter whose upvote was accepted
	downAnswer string // shown to a voter whose downvote was accepted

	// apply carries out the moderation action once the vote has passed, and
	// reports whether it succeeded. It is a method expression, so the table
	// stays immutable and the App is passed per call.
	apply func(a *App, ctx context.Context, b *bot.Bot, s *BanInfo) bool
}

var voteTypes = map[uint8]voteType{
	BAN: {
		upText:     "За бан",
		downText:   "Против бана",
		upAnswer:   "Голос за бан принят",
		downAnswer: "Голос против бана принят",
		apply:      (*App).banUser,
	},
	MUTE: {
		upText:     "За мут",
		downText:   "Против мута",
		upAnswer:   "Голос за мут принят",
		downAnswer: "Голос против мута принят",
		apply:      (*App).muteUser,
	},
	TEXT_ONLY: {
		upText:     "Только текст",
		downText:   "Обычный режим",
		upAnswer:   "Голос за режим «только текст» принят",
		downAnswer: "Голос за обычный режим принят",
		apply:      (*App).textOnlyUser,
	},
}

// BanInfo is a running vote: the persisted ban_log record plus in-memory state.
type BanInfo struct {
	storage.BanLogRecord
	cancelPin context.CancelFunc
}

const (
	DEFAULT_SCORE int16 = 3
	LOW_SCORE     int16 = 3
	MID_SCORE     int16 = 5
	HIGH_SCORE    int16 = 10
)

func makeBanMessage(b *BanInfo) string {
	return makeVoteText(b, "блокировку")
}

func calculateRequiredRating(userScore uint32) (requiredScore int16) {
	if userScore < 10 {
		requiredScore = LOW_SCORE
	} else if userScore < 100 {
		requiredScore = MID_SCORE
	} else {
		requiredScore = HIGH_SCORE
	}
	return requiredScore
}

func getBanInfo(ctx context.Context, chatID int64, messageID int64) (*BanInfo, error) {
	return getInfoByMessage(ctx, chatID, messageID, BAN, makeBanMessage)
}

func (a *App) getBanInfoByUsername(ctx context.Context, chatID int64, username string) (*BanInfo, error) {
	user, err := storage.GetRatingFromUsername(ctx, username)
	if err == nil {
		return a.getBanInfoByUserID(ctx, chatID, user.Userid)
	}
	userInfo, mtErr := a.client.GetUserByUsername(ctx, username)
	if mtErr != nil {
		return nil, err
	}
	return newBanInfoNoDB(chatID, userInfo.UserId, userInfo.Username, BAN, makeBanMessage), nil
}

func (a *App) getBanInfoByUserID(ctx context.Context, chatID int64, userID int64) (*BanInfo, error) {
	return a.getInfoByUserID(ctx, chatID, userID, BAN, makeBanMessage)
}

func (a *App) banUser(ctx context.Context, b *bot.Bot, s *BanInfo) bool {

	// jcart, _ := json.MarshalIndent(s, "", "\t")
	// fmt.Println(string(jcart))

	var banUsertag string

	a.banCache.cacheBanInfo(s.ChatID, s.UserID)
	a.reactionCache.Delete(reactionKey{chatID: s.ChatID, userID: s.UserID})

	if len(s.UserName) != 0 {
		banUsertag = fmt.Sprintf("@%s", tgutil.Escape(s.UserName))
	} else {
		helperUser, err := storage.GetUser(ctx, s.UserID)
		if err != nil {
			banUsertag = fmt.Sprintf("[Пользователь вне базы](tg://user?id=%d)", s.UserID)
		} else {
			banUsertag = toClickableUsername(helperUser)
		}
	}

	result, err := b.BanChatMember(ctx, &bot.BanChatMemberParams{
		ChatID: s.ChatID,
		UserID: s.UserID,
	})
	if err != nil {
		zap.S().Infof("[banUser] BanChatMember failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
	}

	if !result && len(s.UserName) != 0 {
		// Use the MTproto client to try ban
		a.settingsMux.Lock()
		chatSettings := a.getChatSettings(ctx, s.ChatID)
		result, err = a.client.BanUser(ctx, chatSettings.ChatID, chatSettings.ChatAccessHash, s.UserName)
		a.settingsMux.Unlock()
		if err != nil {
			zap.S().Infof("[banUser] MTProto fallback ban failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
		}
	}
	resultText := "Заблокирован успешно"
	if !result {
		resultText = "Не удалось заблокировать"
	}
	report := a.buildModerationReport(ctx, s.ChatID, s.OwnerID, resultText, banUsertag)

	userMessages, _ := storage.GetUserLastDaysMessages(ctx, s.UserID, s.ChatID, 2)
	report, recentIDs := appendRecentMessages(report, userMessages)

	// Delete the vote/request/target messages plus the user's recent messages.
	deleteIDs := append([]int64{s.TargetMessageID, s.VoteMessageID, s.RequestMessageID}, recentIDs...)
	deleteMessagesConcurrently(ctx, b, s.ChatID, deleteIDs, "banUser")

	storage.PushBanLog(ctx, &s.BanLogRecord)
	a.sendReportToRecipients(ctx, b, s.ChatID, report,
		getBanMessageKeyboard(s.ChatID, s.UserID, s.VoteMessageID), "banUser")

	if result {
		a.reactionCache.Delete(reactionKey{chatID: s.ChatID, userID: s.UserID})
		// Also strip the banned user's reactions so they stop counting. A ban
		// through the MTProto username fallback may have no numeric ID.
		if s.UserID == 0 {
			return result
		}
		if ok, err := clearUserReactions(ctx, b, s.ChatID, s.UserID); err != nil || !ok {
			zap.S().Infof("[banUser] clearUserReactions failed: userID=%d chatID=%d: %v", s.UserID, s.ChatID, err)
		}
	}
	return result
}
