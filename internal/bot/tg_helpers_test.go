package bot

import (
	"testing"

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
