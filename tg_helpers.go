package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

var (
	publicGroupRX = regexp.MustCompile(`^-100`)
	mdRegex       = regexp.MustCompile(`(['_~>#!=\-])`)
)

// quoteText escapes text and formats each line as a Telegram block-quote (">line").
func quoteText(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = ">" + escape(line)
	}
	return strings.Join(lines, "\n")
}

func escape(line string) string {
	first_step := regexp.QuoteMeta(line)
	second_step := strings.ReplaceAll(first_step, "`", "\\`")
	return mdRegex.ReplaceAllString(second_step, "\\$1")
}

type Chat struct {
	ChatID   int64
	ChatName string
}

func makePublicGroupString(groupID int64) string {
	// https://gist.github.com/nafiesl/4ad622f344cd1dc3bb1ecbe468ff9f8a
	// groupID has leading -100
	return publicGroupRX.ReplaceAllString(strconv.FormatInt(groupID, 10), "")
}

func getVoteButtons(upvotes int, downvotes int, textType uint8) *models.InlineKeyboardMarkup {

	switch textType {
	case BAN:
		{
			return &models.InlineKeyboardMarkup{
				InlineKeyboard: [][]models.InlineKeyboardButton{
					{
						{Text: fmt.Sprintf("За бан (%d)", upvotes), CallbackData: "button_upvote"},
						{Text: fmt.Sprintf("Против бана (%d)", downvotes), CallbackData: "button_downvote"},
					},
				},
			}
		}
	case MUTE:
		{
			return &models.InlineKeyboardMarkup{
				InlineKeyboard: [][]models.InlineKeyboardButton{
					{
						{Text: fmt.Sprintf("За мут (%d)", upvotes), CallbackData: "button_upvote"},
						{Text: fmt.Sprintf("Против мута (%d)", downvotes), CallbackData: "button_downvote"},
					},
				},
			}
		}
	case TEXT_ONLY:
		{
			return &models.InlineKeyboardMarkup{
				InlineKeyboard: [][]models.InlineKeyboardButton{
					{
						{Text: fmt.Sprintf("Только текст (%d)", upvotes), CallbackData: "button_upvote"},
						{Text: fmt.Sprintf("Обычный режим (%d)", downvotes), CallbackData: "button_downvote"},
					},
				},
			}
		}
	}

	return &models.InlineKeyboardMarkup{}

}

func getBanMessageKeyboard(chatId int64, userId int64) *models.InlineKeyboardMarkup {
	unbanData, err := marshal(&Item{
		Action: ACTION_UNBAN,
		ChatID: chatId,
		Data:   map[uint8]interface{}{DATA_TYPE_USERID: userId},
	})

	if err != nil {
		log.Printf("[getBanMessageKeyboard] marshal error for chatID=%d userID=%d: %v", chatId, userId, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}

	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "Разблокировать", CallbackData: fmt.Sprintf("b_%s", unbanData)},
			},
		},
	}
}

func getMuteMessageKeyboard(chatId int64, userId int64) *models.InlineKeyboardMarkup {
	unmuteData, err := marshal(&Item{
		Action: ACTION_UNMUTE,
		ChatID: chatId,
		Data:   map[uint8]interface{}{DATA_TYPE_USERID: userId},
	})

	if err != nil {
		log.Printf("[getMuteMessageKeyboard] marshal error for chatID=%d userID=%d: %v", chatId, userId, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}

	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "Снять мут", CallbackData: fmt.Sprintf("b_%s", unmuteData)},
			},
		},
	}
}

func getChatListKeyboard(chatList []Chat) *models.InlineKeyboardMarkup {
	buttons := make([][]models.InlineKeyboardButton, len(chatList)+1)
	for k, v := range chatList {
		showChat, err := marshal(&Item{
			Action: ACTION_SHOW_CHAT_ID,
			ChatID: v.ChatID,
		})
		if err != nil {
			log.Printf("[getChatListKeyboard] marshal error for chatID=%d: %v", v.ChatID, err)
			continue
		}
		buttons[k] = []models.InlineKeyboardButton{{Text: v.ChatName, CallbackData: fmt.Sprintf("b_%s", showChat)}}
	}
	refresh, err := marshal(&Item{
		Action: ACTION_SHOW_CHAT_LIST,
	})
	if err != nil {
		log.Printf("[getChatListKeyboard] marshal error for refresh button: %v", err)
		return nil
	}
	buttons[len(chatList)] = []models.InlineKeyboardButton{{Text: "🗘 Обновить", CallbackData: fmt.Sprintf("b_%s", refresh)}}
	return &models.InlineKeyboardMarkup{InlineKeyboard: buttons}

}

func getChatActionsKeyboard(chatID int64) *models.InlineKeyboardMarkup {
	pauseChat, err := marshal(&Item{
		Action: ACTION_PAUSE_CHAT,
		ChatID: chatID,
	})
	if err != nil {
		log.Printf("[getChatActionsKeyboard] marshal error for pause button chatID=%d: %v", chatID, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	unPauseChat, err := marshal(&Item{
		Action: ACTION_UNPAUSE_CHAT,
		ChatID: chatID,
	})
	if err != nil {
		log.Printf("[getChatActionsKeyboard] marshal error for unpause button chatID=%d: %v", chatID, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	enableLog, err := marshal(&Item{
		Action: ACTION_ENABLED_LOG,
		ChatID: chatID,
	})
	if err != nil {
		log.Printf("[getChatActionsKeyboard] marshal error for enable-log button chatID=%d: %v", chatID, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	disableLog, err := marshal(&Item{
		Action: ACTION_DISABLED_LOG,
		ChatID: chatID,
	})
	if err != nil {
		log.Printf("[getChatActionsKeyboard] marshal error for disable-log button chatID=%d: %v", chatID, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	refresh, err := marshal(&Item{
		Action: ACTION_SHOW_CHAT_LIST,
	})
	if err != nil {
		log.Printf("[getChatActionsKeyboard] marshal error for back button chatID=%d: %v", chatID, err)
		return &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		}
	}
	leaveChat, err := marshal(&Item{
		Action: ACTION_LEAVE_CHAT,
		ChatID: chatID,
	})
	if err != nil {
		log.Printf("[getChatActionsKeyboard] marshal error for leave-chat button chatID=%d: %v", chatID, err)
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

	admins, err := b.GetChatAdministrators(ctx, &bot.GetChatAdministratorsParams{
		ChatID: chat,
	})
	ret = make(map[int64]bool)
	if err != nil {
		log.Printf("[getAdmins] GetChatAdministrators failed for chatID=%d: %v", chat, err)
		return nil, err
	}
	for _, admin := range admins {
		switch admin.Type {
		case models.ChatMemberTypeAdministrator:
			ret[admin.Administrator.User.ID] = true
		case models.ChatMemberTypeOwner:
			ret[admin.Owner.User.ID] = true
		default:
			log.Printf("[getAdmins] unexpected member type %q in chatID=%d", admin.Type, chat)
		}
	}
	return ret, nil
}

// systemMessage sends a message to chatId and auto-deletes it after delaySec seconds.
func systemMessage(ctx context.Context, b *bot.Bot, chatID int64, text string, delaySec int64) {
	sent, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:             chatID,
		Text:               text,
		ParseMode:          models.ParseModeMarkdown,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
	})
	if err != nil {
		log.Printf("[systemMessage] SendMessage failed: chatID=%d: %v", chatID, err)
		return
	}
	sentID := sent.ID
	go delay(ctx, delaySec, func() {
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    chatID,
			MessageID: sentID,
		})
	})
}

func systemAnswerToMessage(ctx context.Context, b *bot.Bot, chatId int64, messageId int, text string, deleteOrigin bool) {

	reply, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatId,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
		ReplyParameters: &models.ReplyParameters{
			ChatID:    chatId,
			MessageID: messageId,
		},
	})
	if err != nil {
		log.Printf("[systemAnswerToMessage] SendMessage failed: chatID=%d messageID=%d: %v", chatId, messageId, err)
		return
	}
	removeChatID := chatId
	removeReplyID := reply.ID
	removeOriginalID := messageId
	go delay(ctx, 30, func() {
		if deleteOrigin {
			b.DeleteMessage(ctx, &bot.DeleteMessageParams{
				ChatID:    removeChatID,
				MessageID: removeOriginalID,
			})
		}
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    removeChatID,
			MessageID: removeReplyID,
		})
	})
}

func unbanUser(ctx context.Context, b *bot.Bot, chatId int64, userId int64) (result bool, err error) {

	//  -1000000000000 -
	// log.Printf("user id %v", userId)
	result, err = b.UnbanChatMember(ctx, &bot.UnbanChatMemberParams{
		ChatID:       chatId,
		UserID:       userId,
		OnlyIfBanned: true,
	})
	return
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
	messages, err := getUserLastNthMessages(ctx, userId, chatId, 20)
	if err != nil {
		return false, err
	}
	for _, msg := range messages {
		_, delErr := b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    chatId,
			MessageID: int(msg.MessageID),
		})
		if delErr != nil {
			log.Printf("[deleteAllMessages] can't delete messageID=%d in chatID=%d: %v", msg.MessageID, chatId, delErr)
		}
	}
	return true, nil
}

func isUserAdmin(ctx context.Context, b *bot.Bot, chatID int64, userID int64, messageChatID int64, messageID int) bool {
	if userID == superAdminID {
		return true
	}

	adminsMux.Lock()
	defer adminsMux.Unlock()

	chatAdmins := checkAdmins(ctx, b, chatID)
	_, rep := chatAdmins[userID]
	if !rep {
		log.Printf("[isUserAdmin] unauthorized admin action: userID=%d in chatID=%d", userID, chatID)
		systemAnswerToMessage(ctx, b, messageChatID, messageID, "Недостаточно прав. Необходимо быть администратором чата.", true)
		return false
	}
	return true
}

func updateUserFragTag(ctx context.Context, b *bot.Bot, chatID int64, ownerID int64) {
	userMakeVote(ctx, ownerID, 1)

	adminsMux.Lock()
	chatAdmins := checkAdmins(ctx, b, chatID)
	_, isAdmin := chatAdmins[ownerID]
	adminsMux.Unlock()

	if isAdmin {
		log.Printf("[updateUserFragTag] skipping tag update: userID=%d is an admin in chatID=%d", ownerID, chatID)
		return
	}

	user, err := getUser(ctx, ownerID)
	if err != nil {
		log.Printf("[updateUserFragTag] getUser failed for userID=%d: %v", ownerID, err)
		return
	}
	title := fmt.Sprintf("frags: %d", user.VoteCounter)
	_, err = b.SetChatMemberTag(ctx, &bot.SetChatMemberTagParams{
		ChatID: chatID,
		UserID: ownerID,
		Tag:    title,
	})
	if err != nil {
		log.Printf("[updateUserFragTag] SetChatMemberTag failed: userID=%d chatID=%d: %v", ownerID, chatID, err)
	}
}

func (user *UserRecord) toClickableUsername() (username string) {
	if len(user.Username) == 0 {
		username = fmt.Sprintf("[%s](tg://user?id=%d)", strings.TrimSpace(escape(user.AltUsername)), user.Uid)
	} else {
		username = fmt.Sprintf("@%s", escape(user.Username))
	}
	return username
}
