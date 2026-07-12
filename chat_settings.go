package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

var (
	settingsMux sync.Mutex
	settings    map[int64]*DynamicSetting

	adminsMux sync.Mutex
	admins    map[int64]map[int64]bool
)

func getChatSettings(ctx context.Context, chatId int64) (chatSettings *DynamicSetting) {
	chatSettings, prs := settings[chatId]
	if !prs {
		name := ""
		username := ""
		chat, err := myBot.GetChat(ctx, &bot.GetChatParams{
			ChatID: chatId,
		})
		if err == nil {
			name = chat.Title
			username = chat.Username
		}
		chatSettings = &DynamicSetting{
			ChatID:        chatId,
			Pause:         false,
			LogRecipients: []int64{},
			ChatName:      name,
			ChatUsername:  username,
		}
		accessHash, err := client.GetAccessHash(ctx, chatSettings.ChatID)
		if err != nil {
			zap.S().Infof("[getChatSettings] GetAccessHash failed for chatID=%d: %v", chatSettings.ChatID, err)
		}
		chatSettings.ChatAccessHash = accessHash
		writeChatSettings(ctx, chatId, chatSettings)
	}
	return chatSettings
}

func getChatNameFromSettings(chatID int64) string {
	settingsMux.Lock()
	defer settingsMux.Unlock()

	var name string
	setting, ok := settings[chatID]
	if ok {
		name = setting.ChatName
	}

	return name
}

func getChatAdmins(ctx context.Context) {
	settingsMux.Lock()
	defer settingsMux.Unlock()
	for k := range settings {
		chat, err := myBot.GetChat(ctx, &bot.GetChatParams{
			ChatID: k,
		})
		if err != nil {
			botRemovedFromChat(ctx, k)
			continue
		}
		updated := false
		if len(settings[k].ChatName) == 0 {
			settings[k].ChatName = chat.Title
			updated = true
		}
		if len(settings[k].ChatUsername) == 0 {
			settings[k].ChatUsername = chat.Username
			updated = true
		}
		if updated {
			writeChatSettings(ctx, k, settings[k])
		}

		adminsList, err := getAdmins(ctx, myBot, k)
		if err != nil {
			botRemovedFromChat(ctx, k)
			continue
		}
		adminsMux.Lock()
		admins[k] = adminsList
		adminsMux.Unlock()
	}
}

func checkAdmins(ctx context.Context, b *bot.Bot, chatID int64) (chatAdmins map[int64]bool) {
	chatAdmins, rep := admins[chatID]
	if !rep {
		var err error
		chatAdmins, err = getAdmins(ctx, b, chatID)
		if err != nil {
			return make(map[int64]bool)
		}
		admins[chatID] = chatAdmins
	}
	return chatAdmins
}

func botRemovedFromChat(ctx context.Context, chatID int64) {
	chatSettings, ok := settings[chatID]
	if !ok {
		chatSettings = &DynamicSetting{ChatName: "unknown name", ChatUsername: "unknown username"}
	}

	name := chatSettings.ChatName
	username := chatSettings.ChatUsername
	if ok {
		delete(settings, chatID)
	}

	deleteChatSettings(ctx, chatID)
	// adminsMux.Lock()
	_, ok = admins[chatID]
	if ok {
		delete(admins, chatID)
	}
	// adminsMux.Unlock()

	// sessionsMux.Lock()
	delete(banCache, chatID)
	// sessionsMux.Unlock()

	myBot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: 198082233,
		Text:   fmt.Sprintf("Bot removed from chat \"%s\" (@%s, id: %d)", name, username, chatID),
	})
}

func pauseHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	// CHECK
	adminsMux.Lock()
	chatAdmins := checkAdmins(ctx, b, update.Message.Chat.ID)
	_, rep := chatAdmins[update.Message.From.ID]
	adminsMux.Unlock()

	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    update.Message.Chat.ID,
		MessageID: update.Message.ID,
	})
	if !rep {
		return
	}
	zap.S().Infof("[pauseHandler] userID=%d in chatID=%d: %q", update.Message.From.ID, update.Message.Chat.ID, update.Message.Text)

	settingsMux.Lock()
	chatSettings := getChatSettings(ctx, update.Message.Chat.ID)
	var message string
	if strings.Contains(update.Message.Text, "enable") {
		chatSettings.Pause = true
		message = "Режим паузы активирован"
	} else {
		chatSettings.Pause = false
		message = "Режим паузы деактивирован"
	}
	writeChatSettings(ctx, update.Message.Chat.ID, chatSettings)
	settingsMux.Unlock()

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
func setChannelHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	adminsMux.Lock()
	chatAdmins := checkAdmins(ctx, b, update.Message.Chat.ID)
	_, rep := chatAdmins[update.Message.From.ID]
	adminsMux.Unlock()

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

	settingsMux.Lock()
	chatSettings := getChatSettings(ctx, update.Message.Chat.ID)
	chatSettings.LinkedChannelUsername = channelUsername
	writeChatSettings(ctx, update.Message.Chat.ID, chatSettings)
	settingsMux.Unlock()

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
