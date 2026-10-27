package bot

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

// sendOption tweaks how systemMessage/systemAnswerToMessage deliver an answer.
// Variadic options keep the existing call sites untouched: without options the
// answer is a regular public message.
type sendOption func(*sendConfig)

type sendConfig struct {
	// receiverID, when non-zero, asks for an ephemeral message visible only to
	// this user (and the bot).
	receiverID int64
	// originEphemeralID is the ephemeral ID of the command being answered when
	// that command itself is ephemeral.
	originEphemeralID int
}

// ephemeralTo delivers the answer as an ephemeral message for receiverID.
func ephemeralTo(receiverID int64) sendOption {
	return func(c *sendConfig) { c.receiverID = receiverID }
}

// ephemeralFor delivers the answer ephemerally to the author of msg and, when
// msg is itself an ephemeral command, replies to it by its ephemeral ID.
// Messages sent on behalf of a chat (anonymous admins, channels) have a
// service bot in From, so they get a regular answer instead.
func ephemeralFor(msg *models.Message) sendOption {
	return func(c *sendConfig) {
		if msg == nil {
			return
		}
		if msg.From != nil && msg.SenderChat == nil {
			c.receiverID = msg.From.ID
		}
		c.originEphemeralID = msg.EphemeralMessageID
	}
}

// regularMessageID returns the ID usable with DeleteMessage/EditMessageText,
// or 0 for ephemeral messages, whose message_id does not address them.
func regularMessageID(msg *models.Message) int {
	if msg == nil || msg.EphemeralMessageID != 0 {
		return 0
	}
	return msg.ID
}

// deleteCommand removes the command message msg unless it is ephemeral.
func deleteCommand(ctx context.Context, b *bot.Bot, msg *models.Message) {
	sentRef{chatID: msg.Chat.ID, messageID: regularMessageID(msg)}.delete(ctx, b)
}

func resolveSendConfig(opts []sendOption) sendConfig {
	var c sendConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// ephemeralAllowed reports whether an ephemeral message makes sense for chatID:
// it needs a receiver and a group (private chats have positive IDs).
func (c sendConfig) ephemeralAllowed(chatID int64) bool {
	return c.receiverID != 0 && chatID < 0
}

// replyParams builds the reply target for an answer to messageID. Ephemeral
// messages cannot carry chat_id and reply to an ephemeral command by its
// ephemeral ID. A regular message cannot reply to an ephemeral command, and a
// command with neither ID yields no reply at all.
func (c sendConfig) replyParams(chatID int64, messageID int, ephemeral bool) *models.ReplyParameters {
	switch {
	case c.originEphemeralID != 0 && ephemeral:
		return &models.ReplyParameters{EphemeralMessageID: c.originEphemeralID}
	case c.originEphemeralID != 0 || messageID == 0:
		return nil
	case ephemeral:
		return &models.ReplyParameters{MessageID: messageID}
	}
	return &models.ReplyParameters{ChatID: chatID, MessageID: messageID}
}

// sentRef identifies a delivered system message, regular or ephemeral.
type sentRef struct {
	chatID      int64
	messageID   int   // regular message
	receiverID  int64 // ephemeral message receiver
	ephemeralID int   // ephemeral message ID
}

// delete removes the message with the API matching its kind. IDs that were
// never assigned are skipped, so DeleteMessage is never called with 0.
func (r sentRef) delete(ctx context.Context, b *bot.Bot) {
	switch {
	case r.receiverID != 0:
		if r.ephemeralID == 0 {
			return
		}
		b.DeleteEphemeralMessage(ctx, &bot.DeleteEphemeralMessageParams{
			ChatID:             r.chatID,
			ReceiverUserID:     r.receiverID,
			EphemeralMessageID: r.ephemeralID,
		})
	case r.messageID != 0:
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    r.chatID,
			MessageID: r.messageID,
		})
	}
}

// sendSystem sends params to chatID, ephemerally when the options ask for it
// and the chat allows it. A failed ephemeral send falls back to a regular
// message so the user still gets the answer. messageID is the command being
// answered (0 for none). It returns a reference for later deletion.
func sendSystem(ctx context.Context, b *bot.Bot, chatID int64, messageID int, params *bot.SendMessageParams, cfg sendConfig) (sentRef, error) {
	params.ChatID = chatID
	if cfg.ephemeralAllowed(chatID) {
		params.ReplyParameters = cfg.replyParams(chatID, messageID, true)
		params.EphemeralMessageParameters = &models.EphemeralMessageParameters{ReceiverUserID: cfg.receiverID}
		sent, err := b.SendMessage(ctx, params)
		if err == nil {
			return sentRef{chatID: chatID, receiverID: cfg.receiverID, ephemeralID: sent.EphemeralMessageID}, nil
		}
		zap.S().Infof("[sendSystem] ephemeral SendMessage failed, falling back to regular: chatID=%d receiverID=%d: %v", chatID, cfg.receiverID, err)
		params.EphemeralMessageParameters = nil
	}
	params.ReplyParameters = cfg.replyParams(chatID, messageID, false)
	sent, err := b.SendMessage(ctx, params)
	if err != nil {
		return sentRef{}, err
	}
	return sentRef{chatID: chatID, messageID: sent.ID}, nil
}
