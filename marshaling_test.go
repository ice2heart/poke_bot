package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		item *Item
	}{
		{
			name: "action only",
			item: &Item{Action: ACTION_UNBAN, ChatID: -1001234567890},
		},
		{
			name: "with user ID",
			item: &Item{
				Action: ACTION_UNMUTE,
				ChatID: -1009999999999,
				Data:   map[uint8]interface{}{DATA_TYPE_USERID: int64(555444333)},
			},
		},
		{
			name: "no data field omitted",
			item: &Item{Action: ACTION_PAUSE_CHAT, ChatID: -1000000000001},
		},
		{
			name: "zero chat ID",
			item: &Item{Action: ACTION_SHOW_CHAT_LIST},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := marshal(tt.item)
			require.NoError(t, err)
			assert.NotEmpty(t, encoded)

			decoded, err := unmarshal(encoded)
			require.NoError(t, err)
			require.NotNil(t, decoded)

			assert.Equal(t, tt.item.Action, decoded.Action)
			assert.Equal(t, tt.item.ChatID, decoded.ChatID)
		})
	}
}

// TestMarshalUnmarshalUserID verifies that a user ID stored in Data survives a
// round-trip and can be retrieved correctly via getInt.
func TestMarshalUnmarshalUserID(t *testing.T) {
	const userID = int64(1234567890123)
	original := &Item{
		Action: ACTION_UNBAN,
		ChatID: -1001111111111,
		Data:   map[uint8]interface{}{DATA_TYPE_USERID: userID},
	}

	encoded, err := marshal(original)
	require.NoError(t, err)

	decoded, err := unmarshal(encoded)
	require.NoError(t, err)

	raw, ok := decoded.Data[DATA_TYPE_USERID]
	require.True(t, ok, "DATA_TYPE_USERID key missing after round-trip")
	assert.Equal(t, userID, getInt(raw))
}

func TestUnmarshalErrors(t *testing.T) {
	t.Run("invalid base64", func(t *testing.T) {
		_, err := unmarshal("!!!not-base64!!!")
		assert.Error(t, err)
	})

	t.Run("valid base64 but invalid msgpack", func(t *testing.T) {
		// base64 of raw bytes that are not valid msgpack
		_, err := unmarshal("dGhpcyBpcyBub3QgbXNncGFjaw==") // "this is not msgpack"
		assert.Error(t, err)
	})
}

func TestGetInt(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  int64
	}{
		{"int", int(42), 42},
		{"uint", uint(42), 42},
		{"int8", int8(127), 127},
		{"int16", int16(1000), 1000},
		{"int32", int32(100000), 100000},
		{"int64", int64(1234567890123), 1234567890123},
		{"uint8", uint8(255), 255},
		{"uint16", uint16(60000), 60000},
		{"uint32", uint32(4000000000), 4000000000},
		{"unknown type returns 0", "string", 0},
		{"nil returns 0", nil, 0},
		{"zero int", int(0), 0},
		{"negative int32", int32(-500), -500},
		{"negative int64", int64(-9999999999), -9999999999},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, getInt(tt.input))
		})
	}
}
