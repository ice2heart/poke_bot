package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/ice2heart/poke_bot/internal/storage"
	"github.com/ice2heart/poke_bot/internal/tgutil"
	"go.uber.org/zap"
)

type Chat struct {
	ChatID   int64
	ChatName string
}

func getVoteButtons(upvotes int, downvotes int, textType uint8) *models.InlineKeyboardMarkup {
	vt, ok := voteTypes[textType]
	if !ok {
		zap.S().Infof("[getVoteButtons] unknown vote type %d", textType)
		return &models.InlineKeyboardMarkup{}
	}
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: fmt.Sprintf("%s (%d)", vt.upText, upvotes), CallbackData: BUTTON_UPVOTE},
				{Text: fmt.Sprintf("%s (%d)", vt.downText, downvotes), CallbackData: BUTTON_DOWNVOTE},
			},
		},
	}
}

// showVotersButton builds the "who voted" button for a report keyboard, or
// nil when the callback data cannot be marshaled.
func showVotersButton(chatId int64, voteMessageId int64) []models.InlineKeyboardButton {
	votersData, err := tgutil.Marshal(&tgutil.Item{
		Action: tgutil.ACTION_SHOW_VOTERS,
		ChatID: chatId,
		Data:   map[uint8]interface{}{tgutil.DATA_TYPE_MSGID: voteMessageId},
	})
	if err != nil {
		zap.S().Infof("[showVotersButton] marshal error for chatID=%d voteMessageID=%d: %v", chatId, voteMessageId, err)
		return nil
	}
	return []models.InlineKeyboardButton{
		{Text: "Кто голосовал", CallbackData: fmt.Sprintf("b_%s", votersData)},
	}
}

func getBanMessageKeyboard(chatId int64, userId int64, voteMessageId int64) *models.InlineKeyboardMarkup {
	unbanData, err := tgutil.Marshal(&tgutil.Item{
		Action: tgutil.ACTION_UNBAN,
		ChatID: chatId,
		Data:   map[uint8]interface{}{tgutil.DATA_TYPE_USERID: userId},
	})

	if err != nil {
		zap.S().Infof("[getBanMessageKeyboard] marshal error for chatID=%d userID=%d: %v", chatId, userId, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "Разблокировать", CallbackData: fmt.Sprintf("b_%s", unbanData)},
		},
	}
	if votersRow := showVotersButton(chatId, voteMessageId); votersRow != nil {
		keyboard = append(keyboard, votersRow)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func getMuteMessageKeyboard(chatId int64, userId int64, voteMessageId int64) *models.InlineKeyboardMarkup {
	unmuteData, err := tgutil.Marshal(&tgutil.Item{
		Action: tgutil.ACTION_UNMUTE,
		ChatID: chatId,
		Data:   map[uint8]interface{}{tgutil.DATA_TYPE_USERID: userId},
	})

	if err != nil {
		zap.S().Infof("[getMuteMessageKeyboard] marshal error for chatID=%d userID=%d: %v", chatId, userId, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "Снять мут", CallbackData: fmt.Sprintf("b_%s", unmuteData)},
		},
	}
	if votersRow := showVotersButton(chatId, voteMessageId); votersRow != nil {
		keyboard = append(keyboard, votersRow)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func getChatListKeyboard(chatList []Chat) *models.InlineKeyboardMarkup {
	buttons := make([][]models.InlineKeyboardButton, len(chatList)+1)
	for k, v := range chatList {
		showChat, err := tgutil.Marshal(&tgutil.Item{
			Action: tgutil.ACTION_SHOW_CHAT_ID,
			ChatID: v.ChatID,
		})
		if err != nil {
			zap.S().Infof("[getChatListKeyboard] marshal error for chatID=%d: %v", v.ChatID, err)
			continue
		}
		buttons[k] = []models.InlineKeyboardButton{{Text: v.ChatName, CallbackData: fmt.Sprintf("b_%s", showChat)}}
	}
	refresh, err := tgutil.Marshal(&tgutil.Item{
		Action: tgutil.ACTION_SHOW_CHAT_LIST,
	})
	if err != nil {
		zap.S().Infof("[getChatListKeyboard] marshal error for refresh button: %v", err)
		return nil
	}
	buttons[len(chatList)] = []models.InlineKeyboardButton{{Text: "🗘 Обновить", CallbackData: fmt.Sprintf("b_%s", refresh)}}
	return &models.InlineKeyboardMarkup{InlineKeyboard: buttons}

}

func getChatActionsKeyboard(chatID int64) *models.InlineKeyboardMarkup {
	pauseChat, err := tgutil.Marshal(&tgutil.Item{
		Action: tgutil.ACTION_PAUSE_CHAT,
		ChatID: chatID,
	})
	if err != nil {
		zap.S().Infof("[getChatActionsKeyboard] marshal error for pause button chatID=%d: %v", chatID, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	unPauseChat, err := tgutil.Marshal(&tgutil.Item{
		Action: tgutil.ACTION_UNPAUSE_CHAT,
		ChatID: chatID,
	})
	if err != nil {
		zap.S().Infof("[getChatActionsKeyboard] marshal error for unpause button chatID=%d: %v", chatID, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	enableLog, err := tgutil.Marshal(&tgutil.Item{
		Action: tgutil.ACTION_ENABLED_LOG,
		ChatID: chatID,
	})
	if err != nil {
		zap.S().Infof("[getChatActionsKeyboard] marshal error for enable-log button chatID=%d: %v", chatID, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	disableLog, err := tgutil.Marshal(&tgutil.Item{
		Action: tgutil.ACTION_DISABLED_LOG,
		ChatID: chatID,
	})
	if err != nil {
		zap.S().Infof("[getChatActionsKeyboard] marshal error for disable-log button chatID=%d: %v", chatID, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	refresh, err := tgutil.Marshal(&tgutil.Item{
		Action: tgutil.ACTION_SHOW_CHAT_LIST,
	})
	if err != nil {
		zap.S().Infof("[getChatActionsKeyboard] marshal error for back button chatID=%d: %v", chatID, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	leaveChat, err := tgutil.Marshal(&tgutil.Item{
		Action: tgutil.ACTION_LEAVE_CHAT,
		ChatID: chatID,
	})
	if err != nil {
		zap.S().Infof("[getChatActionsKeyboard] marshal error for leave-chat button chatID=%d: %v", chatID, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "Поставить чат на паузу", CallbackData: fmt.Sprintf("b_%s", pauseChat)},
				{Text: "Снять чат с паузы", CallbackData: fmt.Sprintf("b_%s", unPauseChat)},
			},
			{
				{Text: "Включить логирование", CallbackData: fmt.Sprintf("b_%s", enableLog)},
				{Text: "Выключить логирование", CallbackData: fmt.Sprintf("b_%s", disableLog)},
			},
			{
				{Text: "Выйти из чата", CallbackData: fmt.Sprintf("b_%s", leaveChat)},
				{Text: "К списку чатов", CallbackData: fmt.Sprintf("b_%s", refresh)},
			},
		},
	}

}

// getLikesKeyboard builds the prev/next navigation row for a paginated
// /likes page. hasPrev/hasNext control which buttons are shown.
func getLikesKeyboard(chatID int64, page int, hasPrev bool, hasNext bool) *models.InlineKeyboardMarkup {
	var row []models.InlineKeyboardButton

	if hasPrev {
		prevData, err := tgutil.Marshal(&tgutil.Item{
			Action: tgutil.ACTION_LIKES_PAGE,
			ChatID: chatID,
			Data:   map[uint8]interface{}{tgutil.DATA_TYPE_PAGE: page - 1},
		})
		if err != nil {
			zap.S().Infof("[getLikesKeyboard] marshal error for prev button chatID=%d page=%d: %v", chatID, page-1, err)
		} else {
			row = append(row, models.InlineKeyboardButton{Text: "◀ Пред", CallbackData: fmt.Sprintf("b_%s", prevData)})
		}
	}
	if hasNext {
		nextData, err := tgutil.Marshal(&tgutil.Item{
			Action: tgutil.ACTION_LIKES_PAGE,
			ChatID: chatID,
			Data:   map[uint8]interface{}{tgutil.DATA_TYPE_PAGE: page + 1},
		})
		if err != nil {
			zap.S().Infof("[getLikesKeyboard] marshal error for next button chatID=%d page=%d: %v", chatID, page+1, err)
		} else {
			row = append(row, models.InlineKeyboardButton{Text: "След ▶", CallbackData: fmt.Sprintf("b_%s", nextData)})
		}
	}
	if len(row) == 0 {
		return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row}}
}

func getChatName(ctx context.Context, b *bot.Bot, chatID int64) string {
	// TODO: make it cacheable
	chatInfo, err := b.GetChat(ctx, &bot.GetChatParams{
		ChatID: chatID,
	})
	if err != nil {
		return fmt.Sprintf("Unknown chat with id %d", chatID)
	}
	return chatInfo.Title
}

func getAdmins(ctx context.Context, b *bot.Bot, chat int64) (ret map[int64]bool, err error) {

	members, err := b.GetChatAdministrators(ctx, &bot.GetChatAdministratorsParams{
		ChatID: chat,
	})
	ret = make(map[int64]bool)
	if err != nil {
		zap.S().Infof("[getAdmins] GetChatAdministrators failed for chatID=%d: %v", chat, err)
		return nil, err
	}
	for _, admin := range members {
		switch admin.Type {
		case models.ChatMemberTypeAdministrator:
			ret[admin.Administrator.User.ID] = true
		case models.ChatMemberTypeOwner:
			ret[admin.Owner.User.ID] = true
		default:
			zap.S().Infof("[getAdmins] unexpected member type %q in chatID=%d", admin.Type, chat)
		}
	}
	return ret, nil
}

// systemMessage sends a message to chatId and auto-deletes it after delaySec seconds.
// Pass ephemeralTo(userID) to show it to a single user only.
func systemMessage(ctx context.Context, b *bot.Bot, chatID int64, text string, delaySec int64, opts ...sendOption) {
	sent, err := sendSystem(ctx, b, chatID, 0, &bot.SendMessageParams{
		Text:               text,
		ParseMode:          models.ParseModeMarkdown,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
	}, resolveSendConfig(opts))
	if err != nil {
		zap.S().Infof("[systemMessage] SendMessage failed: chatID=%d: %v", chatID, err)
		return
	}
	go delay(ctx, delaySec, func() {
		sent.delete(ctx, b)
	})
}

// systemAnswerToMessage replies to messageId and auto-deletes the reply (and
// optionally the original) after delaySec seconds. Pass ephemeralFor(msg) or
// ephemeralTo(userID) to show the reply to a single user only.
func systemAnswerToMessage(ctx context.Context, b *bot.Bot, chatId int64, messageId int, text string, deleteOrigin bool, delaySec int64, opts ...sendOption) {
	cfg := resolveSendConfig(opts)
	reply, err := sendSystem(ctx, b, chatId, messageId, &bot.SendMessageParams{
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	}, cfg)
	if err != nil {
		zap.S().Infof("[systemAnswerToMessage] SendMessage failed: chatID=%d messageID=%d: %v", chatId, messageId, err)
		return
	}
	origin := sentRef{chatID: chatId, messageID: messageId}
	if cfg.originEphemeralID != 0 {
		// An ephemeral command can't be deleted and its message_id would
		// address some unrelated regular message.
		origin.messageID = 0
	}
	go delay(ctx, delaySec, func() {
		if deleteOrigin {
			origin.delete(ctx, b)
		}
		reply.delete(ctx, b)
	})
}

func unbanUser(ctx context.Context, b *bot.Bot, chatId int64, userId int64) (result bool, err error) {
	if userId < 0 {
		// Negative IDs are channels/sender chats — use UnbanChatSenderChat.
		return b.UnbanChatSenderChat(ctx, &bot.UnbanChatSenderChatParams{
			ChatID:       chatId,
			SenderChatID: userId,
		})
	}
	return b.UnbanChatMember(ctx, &bot.UnbanChatMemberParams{
		ChatID:       chatId,
		UserID:       userId,
		OnlyIfBanned: true,
	})
}

func unmuteUser(ctx context.Context, b *bot.Bot, chatId int64, userId int64) (result bool, err error) {
	// result, err = b.UnbanChatMember(ctx, &bot.UnbanChatMemberParams{
	// 	ChatID:       chatId,
	// 	UserID:       userId,
	// 	OnlyIfBanned: true,
	// })
	result, err = b.RestrictChatMember(ctx, &bot.RestrictChatMemberParams{
		ChatID:                        chatId,
		UserID:                        userId,
		UseIndependentChatPermissions: false,
		Permissions: &models.ChatPermissions{
			CanSendOtherMessages:  true,
			CanAddWebPagePreviews: true,
			CanSendPolls:          true,
		},
		UntilDate: 0,
	})
	return
}

func deleteAllMessages(ctx context.Context, b *bot.Bot, chatId int64, userId int64) (result bool, err error) {
	messages, err := storage.GetUserLastNthMessages(ctx, userId, chatId, 20)
	if err != nil {
		return false, err
	}
	for _, msg := range messages {
		_, delErr := b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    chatId,
			MessageID: int(msg.MessageID),
		})
		if delErr != nil {
			zap.S().Infof("[deleteAllMessages] can't delete messageID=%d in chatID=%d: %v", msg.MessageID, chatId, delErr)
		}
	}
	return true, nil
}

func (a *App) isUserAdmin(ctx context.Context, b *bot.Bot, chatID int64, userID int64, messageChatID int64, messageID int, opts ...sendOption) bool {
	if userID == a.superAdminID {
		return true
	}

	a.adminsMux.Lock()
	defer a.adminsMux.Unlock()

	chatAdmins := a.checkAdmins(ctx, b, chatID)
	_, rep := chatAdmins[userID]
	if !rep {
		zap.S().Infof("[isUserAdmin] unauthorized admin action: userID=%d in chatID=%d", userID, chatID)
		systemAnswerToMessage(ctx, b, messageChatID, messageID, tgutil.Escape("Недостаточно прав. Необходимо быть администратором чата."), true, 30,
			append([]sendOption{ephemeralTo(userID)}, opts...)...)
		return false
	}
	return true
}

func (a *App) updateUserFragTag(ctx context.Context, b *bot.Bot, chatID int64, ownerID int64) {
	storage.UserMakeVote(ctx, ownerID, 1)

	a.adminsMux.Lock()
	chatAdmins := a.checkAdmins(ctx, b, chatID)
	_, isAdmin := chatAdmins[ownerID]
	a.adminsMux.Unlock()

	if isAdmin {
		zap.S().Infof("[updateUserFragTag] skipping tag update: userID=%d is an admin in chatID=%d", ownerID, chatID)
		return
	}

	user, err := storage.GetUser(ctx, ownerID)
	if err != nil {
		zap.S().Infof("[updateUserFragTag] getUser failed for userID=%d: %v", ownerID, err)
		return
	}
	if user.CustomTag != "" {
		zap.S().Infof("[updateUserFragTag] skipping tag update: userID=%d has custom tag", ownerID)
		return
	}
	title := fmt.Sprintf("frags: %d", user.VoteCounter)
	_, err = b.SetChatMemberTag(ctx, &bot.SetChatMemberTagParams{
		ChatID: chatID,
		UserID: ownerID,
		Tag:    title,
	})
	if err != nil {
		zap.S().Infof("[updateUserFragTag] SetChatMemberTag failed: userID=%d chatID=%d: %v", ownerID, chatID, err)
	}
}

// userTagByID resolves a clickable MarkdownV2 tag for a user ID, falling back
// to a bare tg://user link when the user is not in the database.
func userTagByID(ctx context.Context, uID int64) string {
	user, err := storage.GetUser(ctx, uID)
	if err != nil {
		return fmt.Sprintf("[Пользователь вне базы](tg://user?id=%d)", uID)
	}
	return toClickableUsername(user)
}

func toClickableUsername(user *storage.UserRecord) (username string) {
	if len(user.Username) == 0 {
		username = fmt.Sprintf("[%s](tg://user?id=%d)", strings.TrimSpace(tgutil.Escape(user.AltUsername)), user.Uid)
	} else {
		username = fmt.Sprintf("@%s", tgutil.Escape(user.Username))
	}
	return username
}
