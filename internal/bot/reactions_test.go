package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeleteAllReactionsParams(t *testing.T) {
	p := deleteAllReactionsParams(-100123, 42)
	assert.Equal(t, int64(42), p.UserID)
	assert.Zero(t, p.ActorChatID)

	p = deleteAllReactionsParams(-100123, -1001999)
	assert.Zero(t, p.UserID)
	assert.Equal(t, int64(-1001999), p.ActorChatID)
}

func TestParseReactionTarget(t *testing.T) {
	id, name := parseReactionTarget("@spammer")
	assert.Zero(t, id)
	assert.Equal(t, "spammer", name)

	id, name = parseReactionTarget("12345")
	assert.Equal(t, int64(12345), id)
	assert.Empty(t, name)

	id, name = parseReactionTarget("hello")
	assert.Zero(t, id)
	assert.Empty(t, name)

	id, name = parseReactionTarget("@")
	assert.Zero(t, id)
	assert.Empty(t, name)
}
