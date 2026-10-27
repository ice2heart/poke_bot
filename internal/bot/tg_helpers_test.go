package bot

import (
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ice2heart/poke_bot/internal/tgutil"
)

// TestShowVotersButtonFitsCallbackData ensures the show-voters callback data
// stays within Telegram's 64-byte limit even with worst-case-size IDs.
func TestShowVotersButtonFitsCallbackData(t *testing.T) {
	row := showVotersButton(-1009999999999, 9999999999)
	require.Len(t, row, 1)
	assert.LessOrEqual(t, len(row[0].CallbackData), 64)

	decoded, err := tgutil.Unmarshal(row[0].CallbackData[2:])
	require.NoError(t, err)
	assert.Equal(t, tgutil.ACTION_SHOW_VOTERS, decoded.Action)
	assert.Equal(t, int64(-1009999999999), decoded.ChatID)
	assert.Equal(t, int64(9999999999), tgutil.GetInt(decoded.Data[tgutil.DATA_TYPE_MSGID]))
}

func TestResolveSendConfig(t *testing.T) {
	assert.Equal(t, sendConfig{}, resolveSendConfig(nil))
	assert.Equal(t, int64(7), resolveSendConfig([]sendOption{ephemeralTo(7)}).receiverID)

	cmd := &models.Message{From: &models.User{ID: 42}}
	c := resolveSendConfig([]sendOption{ephemeralFor(cmd)})
	assert.Equal(t, sendConfig{receiverID: 42}, c)

	ephCmd := &models.Message{From: &models.User{ID: 42}, EphemeralMessageID: 9}
	c = resolveSendConfig([]sendOption{ephemeralFor(ephCmd)})
	assert.Equal(t, sendConfig{receiverID: 42, originEphemeralID: 9}, c)

	// The lib's own fixture: an ephemeral message may carry a non-zero message_id.
	ephCmdWithID := &models.Message{ID: 1, From: &models.User{ID: 42}, EphemeralMessageID: 77}
	c = resolveSendConfig([]sendOption{ephemeralFor(ephCmdWithID)})
	assert.Equal(t, sendConfig{receiverID: 42, originEphemeralID: 77}, c)

	anon := &models.Message{From: &models.User{ID: 1087968824}, SenderChat: &models.Chat{ID: -100123}}
	assert.Equal(t, sendConfig{}, resolveSendConfig([]sendOption{ephemeralFor(anon)}), "anonymous admin")

	assert.Equal(t, sendConfig{}, resolveSendConfig([]sendOption{ephemeralFor(nil)}))
}

func TestRegularMessageID(t *testing.T) {
	assert.Equal(t, 5, regularMessageID(&models.Message{ID: 5}))
	assert.Equal(t, 0, regularMessageID(&models.Message{ID: 1, EphemeralMessageID: 77}))
	assert.Equal(t, 0, regularMessageID(nil))
}

func TestEphemeralAllowed(t *testing.T) {
	assert.True(t, sendConfig{receiverID: 1}.ephemeralAllowed(-100123))
	assert.False(t, sendConfig{receiverID: 1}.ephemeralAllowed(555), "private chat")
	assert.False(t, sendConfig{}.ephemeralAllowed(-100123), "no receiver")
}

func TestReplyParams(t *testing.T) {
	c := sendConfig{receiverID: 1}
	assert.Equal(t, &models.ReplyParameters{MessageID: 5}, c.replyParams(-1, 5, true), "ephemeral has no chat_id")
	assert.Equal(t, &models.ReplyParameters{ChatID: int64(-1), MessageID: 5}, c.replyParams(-1, 5, false))
	assert.Nil(t, c.replyParams(-1, 0, true))

	eph := sendConfig{receiverID: 1, originEphemeralID: 9}
	assert.Equal(t, &models.ReplyParameters{EphemeralMessageID: 9}, eph.replyParams(-1, 0, true))
	assert.Equal(t, &models.ReplyParameters{EphemeralMessageID: 9}, eph.replyParams(-1, 1, true), "ephemeral ID wins over message_id")
	assert.Nil(t, eph.replyParams(-1, 1, false), "cannot reply to an ephemeral origin publicly")
}
