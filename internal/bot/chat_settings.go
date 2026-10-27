package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/ice2heart/poke_bot/internal/storage"
	"go.uber.org/zap"
)

func (a *App) getChatSettings(ctx context.Context, chatId int64) (chatSettings *storage.DynamicSetting) {
	chatSettings, prs := a.settings[chatId]
	if !prs {
		name := ""
		username := ""
		chat, err := a.bot.GetChat(ctx, &bot.GetChatParams{
			ChatID: chatId,
		})
		if err == nil {
			name = chat.Title
			username = chat.Username
		}
		chatSettings = &storage.DynamicSetting{
			ChatID:        chatId,
			Pause:         false,
			LogRecipients: []int64{},
			ChatName:      name,
			ChatUsername:  username,
		}
		accessHash, err := a.client.GetAccessHash(ctx, chatSettings.ChatID)
		if err != nil {
			zap.S().Infof("[getChatSettings] GetAccessHash failed for chatID=%d: %v", chatSettings.ChatID, err)
		}
		chatSettings.ChatAccessHash = accessHash
		storage.WriteChatSettings(ctx, chatId, chatSettings)
	}
	return chatSettings
}

func (a *App) getChatNameFromSettings(chatID int64) string {
	a.settingsMux.Lock()
	defer a.settingsMux.Unlock()

	var name string
	setting, ok := a.settings[chatID]
	if ok {
		name = setting.ChatName
	}

	return name
}

func (a *App) getChatAdmins(ctx context.Context) {
	var removed []int64
	defer func() {
		for _, chatID := range removed {
			a.botRemovedFromChat(ctx, chatID)
		}
	}()

	a.settingsMux.Lock()
	defer a.settingsMux.Unlock()
	for k := range a.settings {
		chat, err := a.bot.GetChat(ctx, &bot.GetChatParams{
			ChatID: k,
		})
		if err != nil {
			removed = append(removed, k)
			continue
		}
		updated := false
		if len(a.settings[k].ChatName) == 0 {
			a.settings[k].ChatName = chat.Title
			updated = true
		}
		if len(a.settings[k].ChatUsername) == 0 {
			a.settings[k].ChatUsername = chat.Username
			updated = true
		}
		if updated {
			storage.WriteChatSettings(ctx, k, a.settings[k])
		}

		adminsList, err := getAdmins(ctx, a.bot, k)
		if err != nil {
			removed = append(removed, k)
			continue
		}
		a.adminsMux.Lock()
		a.admins[k] = adminsList
		a.adminsMux.Unlock()
	}
}

func (a *App) checkAdmins(ctx context.Context, b *bot.Bot, chatID int64) (chatAdmins map[int64]bool) {
	chatAdmins, rep := a.admins[chatID]
	if !rep {
		var err error
		chatAdmins, err = getAdmins(ctx, b, chatID)
		if err != nil {
			return make(map[int64]bool)
		}
		a.admins[chatID] = chatAdmins
	}
	return chatAdmins
}

// botRemovedFromChat must be called without settingsMux or adminsMux held.
func (a *App) botRemovedFromChat(ctx context.Context, chatID int64) {
	a.settingsMux.Lock()
	chatSettings, ok := a.settings[chatID]
	if !ok {
		chatSettings = &storage.DynamicSetting{ChatName: "unknown name", ChatUsername: "unknown username"}
	}
	name := chatSettings.ChatName
	username := chatSettings.ChatUsername
	delete(a.settings, chatID)
	a.settingsMux.Unlock()

	storage.DeleteChatSettings(ctx, chatID)

	a.adminsMux.Lock()
	delete(a.admins, chatID)
	a.adminsMux.Unlock()

	a.banCache.drop(chatID)

	a.bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: a.superAdminID,
		Text:   fmt.Sprintf("Bot removed from chat \"%s\" (@%s, id: %d)", name, username, chatID),
	})
}

func (a *App) pauseHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	// CHECK
	a.adminsMux.Lock()
	chatAdmins := a.checkAdmins(ctx, b, update.Message.Chat.ID)
	_, rep := chatAdmins[update.Message.From.ID]
	a.adminsMux.Unlock()

	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    update.Message.Chat.ID,
		MessageID: update.Message.ID,
	})
	if !rep {
		return
	}
	zap.S().Infof("[pauseHandler] userID=%d in chatID=%d: %q", update.Message.From.ID, update.Message.Chat.ID, update.Message.Text)

	a.settingsMux.Lock()
	chatSettings := a.getChatSettings(ctx, update.Message.Chat.ID)
	var message string
	if strings.Contains(update.Message.Text, "enable") {
		chatSettings.Pause = true
		message = "Режим паузы активирован"
	} else {
		chatSettings.Pause = false
		message = "Режим паузы деактивирован"
	}
	storage.WriteChatSettings(ctx, update.Message.Chat.ID, chatSettings)
	a.settingsMux.Unlock()

	replay, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   message,
	})
	if err != nil {
		return
	}
	go delay(ctx, 10, func() {
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    replay.Chat.ID,
			MessageID: replay.ID,
		})
	})

}

// setChannelHandler sets (or clears) the linked channel username for the current chat.
// Usage: /set_channel @channelname  — to set
//
//	/set_channel            — to clear
func (a *App) setChannelHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	a.adminsMux.Lock()
	chatAdmins := a.checkAdmins(ctx, b, update.Message.Chat.ID)
	_, rep := chatAdmins[update.Message.From.ID]
	a.adminsMux.Unlock()

	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    update.Message.Chat.ID,
		MessageID: update.Message.ID,
	})
	if !rep {
		return
	}

	parts := strings.SplitN(update.Message.Text, " ", 2)
	var channelUsername string
	if len(parts) == 2 {
		channelUsername = strings.TrimPrefix(strings.TrimSpace(parts[1]), "@")
	}

	a.settingsMux.Lock()
	chatSettings := a.getChatSettings(ctx, update.Message.Chat.ID)
	chatSettings.LinkedChannelUsername = channelUsername
	storage.WriteChatSettings(ctx, update.Message.Chat.ID, chatSettings)
	a.settingsMux.Unlock()

	zap.S().Infof("[setChannelHandler] chatID=%d linkedChannelUsername=%q set by userID=%d",
		update.Message.Chat.ID, channelUsername, update.Message.From.ID)

	var message string
	if channelUsername == "" {
		message = "Привязанный канал удалён"
	} else {
		message = fmt.Sprintf("Привязанный канал установлен: @%s", channelUsername)
	}
	reply, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   message,
	})
	if err != nil {
		return
	}
	go delay(ctx, 10, func() {
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    reply.Chat.ID,
			MessageID: reply.ID,
		})
	})
}
