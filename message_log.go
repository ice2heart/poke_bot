package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

const (
	MESSAGE_TTL_DAYS = 14
)

func logMessagesMiddleware(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		// jcart, _ := json.MarshalIndent(update, "", "\t")
		// fmt.Println(string(jcart))
		if update.MyChatMember != nil {

			if update.MyChatMember.NewChatMember.Banned != nil || update.MyChatMember.NewChatMember.Left != nil {
				botRemovedFromChat(ctx, update.MyChatMember.Chat.ID)
			}
			if update.MyChatMember.NewChatMember.Member != nil && update.MyChatMember.Chat.ID < 0 {
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: 198082233,
					Text:   fmt.Sprintf("Bot added to chat \"%s\" (@%s)", update.MyChatMember.Chat.Title, update.MyChatMember.Chat.Username),
				})
				settingsMux.Lock()
				getChatSettings(ctx, update.MyChatMember.Chat.ID)
				settingsMux.Unlock()
			}
		}

		if update.Message != nil {
			now := time.Now()

			userID := update.Message.From.ID
			userName := update.Message.From.Username
			altUserName := fmt.Sprintf("%s %s", update.Message.From.FirstName, update.Message.From.LastName)
			if update.Message.SenderChat != nil {
				userID = update.Message.SenderChat.ID
				userName = update.Message.SenderChat.Username
				altUserName = update.Message.SenderChat.Title
			}

			if update.Message.PinnedMessage != nil && userID == b.ID() {
				b.DeleteMessage(ctx, &bot.DeleteMessageParams{
					ChatID:    update.Message.Chat.ID,
					MessageID: update.Message.ID,
				})
				next(ctx, b, update)
				return
			}

			// Detach from the handler context: the bot library may cancel ctx
			// once the handler returns, which would abort these background writes.
			bgCtx := context.WithoutCancel(ctx)
			go userPlusOneMessage(bgCtx, userID, userName, altUserName)
			go saveMessage(bgCtx, &ChatMessage{
				MessageID: int64(update.Message.ID),
				ChatID:    update.Message.Chat.ID,
				UserID:    userID,
				UserName:  userName,
				Text:      buildStoredText(update.Message),
				Date:      uint64(now.AddDate(0, 0, MESSAGE_TTL_DAYS).UnixMilli()),
			})
		}
		if update.EditedMessage != nil {

			storedText := update.EditedMessage.Text
			if len(update.EditedMessage.Caption) != 0 {
				storedText = fmt.Sprintf("Photo, text:\n%s", update.EditedMessage.Caption)
			}
			hiddenUrls := collectHiddenURLs(update.EditedMessage.CaptionEntities, update.EditedMessage.Entities)
			if len(hiddenUrls) != 0 {
				storedText = fmt.Sprintf("%s\n%s", storedText, strings.Join(hiddenUrls, "\n"))
			}
			go updateMessage(context.WithoutCancel(ctx), &ChatMessage{
				MessageID: int64(update.EditedMessage.ID),
				ChatID:    update.EditedMessage.Chat.ID,
				Text:      storedText,
			})

		}

		next(ctx, b, update)
	}
}

// buildStoredText derives the text to store for a message: media messages get
// a description instead, hidden text-link URLs are appended, and captionless
// photos/videos fall back to a placeholder.
func buildStoredText(msg *models.Message) string {
	storedText := msg.Text
	if msg.Sticker != nil {
		storedText = fmt.Sprintf("Sticker: %s, pack: %s", msg.Sticker.Emoji, msg.Sticker.SetName)
	}
	if msg.Animation != nil {
		storedText = fmt.Sprintf("GIF: name %s", msg.Animation.FileName)
	}
	if len(msg.Caption) != 0 {
		storedText = fmt.Sprintf("Photo, text:\n%s", msg.Caption)
	}

	hiddenUrls := collectHiddenURLs(msg.CaptionEntities, msg.Entities)
	if len(hiddenUrls) != 0 {
		storedText = fmt.Sprintf("%s\n%s", storedText, strings.Join(hiddenUrls, "\n"))
	}
	if len(storedText) == 0 {
		if len(msg.Photo) != 0 {
			storedText = "A photo without text"
		}
		if msg.Video != nil {
			storedText = "A video without text"
		}
	}
	return storedText
}

// collectHiddenURLs returns all explicit URL values (text-link entities) from
// the provided entity slices. Plain URL entities are not included as their URL
// is already visible in the message text.
func collectHiddenURLs(entitySlices ...[]models.MessageEntity) []string {
	var urls []string
	for _, entities := range entitySlices {
		for _, v := range entities {
			if len(v.URL) != 0 {
				urls = append(urls, v.URL)
			}
		}
	}
	return urls
}

// extractLinkedMessageIDs scans URL entities in text for Telegram message links
// that belong to the given chat (matched by chatID or chatUsername) and returns
// the referenced message IDs.
func extractLinkedMessageIDs(entities []models.MessageEntity, text string, chatID int64, chatUsername string) []int {
	var ids []int
	for _, v := range entities {
		if v.Type != models.MessageEntityTypeURL {
			continue
		}
		entityText := text[v.Offset : v.Offset+v.Length]
		zap.S().Infof("[extractLinkedMessageIDs] processing URL entity in chatID=%d: %q", chatID, entityText)
		for _, m := range linkRegex.FindAllStringSubmatch(entityText, -1) {
			if m[1] == "" {
				if m[2] != chatUsername {
					zap.S().Infof("[extractLinkedMessageIDs] link chat username mismatch: got %q expected %q in chatID=%d", m[2], chatUsername, chatID)
					continue
				}
			} else {
				parsedID, _ := strconv.ParseInt("-100"+m[2], 10, 64)
				if chatID != parsedID {
					zap.S().Infof("[extractLinkedMessageIDs] link chatID mismatch: got %d expected %d", parsedID, chatID)
					continue
				}
			}
			pokeMessageID, err := strconv.ParseInt(m[3], 10, 64)
			if err != nil {
				zap.S().Infof("[extractLinkedMessageIDs] corrupted messageID %q in URL entity chatID=%d", m[3], chatID)
				continue
			}
			ids = append(ids, int(pokeMessageID))
		}
	}
	return ids
}
