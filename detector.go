package main

import (
	"context"
	"fmt"
	"log"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// detectorCh receives edited-message updates for spam analysis.
var detectorCh = make(chan *models.Update, 128)

// editLinkMinDelay is the minimum edit delay (seconds) that triggers link-spam detection.
const editLinkMinDelay = 120 // 2 minutes

// startDetector runs the detection loop in a goroutine; exits when ctx is cancelled.
func startDetector(ctx context.Context, b *bot.Bot) {
	for {
		select {
		case <-ctx.Done():
			return
		case update := <-detectorCh:
			if update.EditedMessage != nil {
				processDetectorEdit(ctx, b, update)
			}
		}
	}
}

func processDetectorEdit(ctx context.Context, b *bot.Bot, update *models.Update) {
	msg := update.EditedMessage

	editDelaySec := msg.EditDate - msg.Date
	if editDelaySec <= editLinkMinDelay {
		return
	}

	hasLink := false
	for _, e := range msg.Entities {
		if e.Type == models.MessageEntityTypeURL || e.Type == models.MessageEntityTypeTextLink {
			hasLink = true
			break
		}
	}
	if !hasLink {
		for _, e := range msg.CaptionEntities {
			if e.Type == models.MessageEntityTypeURL || e.Type == models.MessageEntityTypeTextLink {
				hasLink = true
				break
			}
		}
	}
	if !hasLink {
		return
	}

	log.Printf("[detector] suspected spam edit: messageID=%d chatID=%d editDelaySec=%d",
		msg.ID, msg.Chat.ID, editDelaySec)

	msgLink := fmt.Sprintf("tg://privatepost?channel=%s&post=%d",
		makePublicGroupString(msg.Chat.ID), msg.ID)
	text := fmt.Sprintf(
		"Подозрительная активность: [сообщение](%s) отредактировано с добавлением ссылки спустя %d с",
		msgLink, editDelaySec,
	)

	sent, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:             msg.Chat.ID,
		Text:               text,
		ParseMode:          models.ParseModeMarkdown,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
	})
	if err != nil {
		log.Printf("[detector] SendMessage failed for chatID=%d messageID=%d: %v",
			msg.Chat.ID, msg.ID, err)
		return
	}

	sentID := sent.ID
	chatID := msg.Chat.ID
	go delay(ctx, 5*60, func() {
		if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    chatID,
			MessageID: sentID,
		}); err != nil {
			log.Printf("[detector] can't delete warning messageID=%d in chatID=%d: %v",
				sentID, chatID, err)
		}
	})
}

// detectorMiddleware feeds all message and edit updates into the detection goroutine.
func detectorMiddleware(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message != nil || update.EditedMessage != nil {
			select {
			case detectorCh <- update:
			default:
				log.Printf("[detector] channel full, dropping update")
			}
		}
		next(ctx, b, update)
	}
}
