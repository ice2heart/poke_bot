package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEntityText(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		offset int
		length int
		want   string
	}{
		{
			name:   "ascii only",
			s:      "/ban username",
			offset: 5,
			length: 8,
			want:   "username",
		},
		{
			name:   "cyrillic mention",
			s:      "/ban Мия",
			offset: 5,
			length: 3,
			want:   "Мия",
		},
		{
			name:   "cyrillic at start",
			s:      "Привет мир",
			offset: 0,
			length: 6,
			want:   "Привет",
		},
		{
			name:   "emoji (surrogate pair, 2 UTF-16 units)",
			s:      "hello 😀 world",
			offset: 6,
			length: 2,
			want:   "😀",
		},
		{
			name:   "text after emoji",
			s:      "😀 test",
			offset: 3,
			length: 4,
			want:   "test",
		},
		{
			name:   "offset past end returns empty",
			s:      "abc",
			offset: 10,
			length: 2,
			want:   "",
		},
		{
			name:   "length reaches end of string",
			s:      "hello",
			offset: 2,
			length: 3,
			want:   "llo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, entityText(tt.s, tt.offset, tt.length))
		})
	}
}

func TestFirstN(t *testing.T) {
	tests := []struct {
		dataString     string
		requiredLength int
		want           string
	}{
		{
			dataString:     "test",
			requiredLength: 6,
			want:           "test",
		},
		{
			dataString:     "Привет я длинная utf-8 строчка",
			requiredLength: 6,
			want:           "Привет",
		},
		{
			dataString:     "Привет я длинная utf-8 строчка",
			requiredLength: 50,
			want:           "Привет я длинная utf-8 строчка",
		},
	}
	for _, tt := range tests {
		t.Run(tt.dataString, func(t *testing.T) {
			if got := firstN(tt.dataString, tt.requiredLength); !assert.Equal(t, got, tt.want) {
				t.Errorf("getMuteDurationText() = %v, want %v", got, tt.want)
			}
		})
	}
}

// wantParsedLink describes the expected outcome for one ParsedLink entry.
type wantParsedLink struct {
	chatID  int64
	msgID   int64
	wantErr bool
}

func TestParseChatLink(t *testing.T) {
	const (
		chatID   = int64(-1001234567890)
		chatName = "mychat"
	)

	tests := []struct {
		name                  string
		link                  string
		chatID                int64
		chatName              string
		linkedChannelUsername string
		want                  []wantParsedLink
	}{
		{
			name:     "public chat match",
			link:     "https://t.me/mychat/42",
			chatID:   chatID,
			chatName: chatName,
			want:     []wantParsedLink{{chatID: chatID, msgID: 42}},
		},
		{
			name:     "public chat username mismatch",
			link:     "https://t.me/wrongchat/42",
			chatID:   chatID,
			chatName: chatName,
			want:     []wantParsedLink{{wantErr: true}},
		},
		{
			name:     "private chat match",
			link:     "https://t.me/c/1234567890/42",
			chatID:   chatID,
			chatName: chatName,
			want:     []wantParsedLink{{chatID: chatID, msgID: 42}},
		},
		{
			name:     "private chat ID mismatch",
			link:     "https://t.me/c/9999999999/42",
			chatID:   chatID,
			chatName: chatName,
			want:     []wantParsedLink{{wantErr: true}},
		},
		{
			name:                  "channel comment link match",
			link:                  "https://t.me/mychannel/100?comment=456",
			chatID:                chatID,
			chatName:              chatName,
			linkedChannelUsername: "mychannel",
			want:                  []wantParsedLink{{chatID: chatID, msgID: 456}},
		},
		{
			name:                  "channel comment link — no linked channel configured",
			link:                  "https://t.me/mychannel/100?comment=456",
			chatID:                chatID,
			chatName:              chatName,
			linkedChannelUsername: "",
			// falls to standard path: "mychannel" != chatName → mismatch
			want: []wantParsedLink{{wantErr: true}},
		},
		{
			name:                  "channel comment link — wrong channel username",
			link:                  "https://t.me/other/100?comment=456",
			chatID:                chatID,
			chatName:              chatName,
			linkedChannelUsername: "mychannel",
			// "other" != linkedChannelUsername, falls to standard path: "other" != chatName → mismatch
			want: []wantParsedLink{{wantErr: true}},
		},
		{
			name:                  "channel link without comment — not treated as comment link",
			link:                  "https://t.me/mychannel/100",
			chatID:                chatID,
			chatName:              chatName,
			linkedChannelUsername: "mychannel",
			// no comment param → falls to standard path: "mychannel" != chatName → mismatch
			want: []wantParsedLink{{wantErr: true}},
		},
		{
			name:     "no link in string",
			link:     "just plain text",
			chatID:   chatID,
			chatName: chatName,
			want:     []wantParsedLink{},
		},
		{
			name:     "multiple links",
			link:     "https://t.me/mychat/1 https://t.me/mychat/2",
			chatID:   chatID,
			chatName: chatName,
			want: []wantParsedLink{
				{chatID: chatID, msgID: 1},
				{chatID: chatID, msgID: 2},
			},
		},
		{
			name:                  "multiple links — one chat, one channel comment",
			link:                  "https://t.me/mychat/1 https://t.me/mychannel/99?comment=200",
			chatID:                chatID,
			chatName:              chatName,
			linkedChannelUsername: "mychannel",
			want: []wantParsedLink{
				{chatID: chatID, msgID: 1},
				{chatID: chatID, msgID: 200},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseChatLink(tt.link, tt.chatID, tt.chatName, tt.linkedChannelUsername)
			require.Len(t, got, len(tt.want))
			for i, w := range tt.want {
				assert.Equal(t, w.wantErr, got[i].err != nil, "result[%d] err presence", i)
				if !w.wantErr {
					assert.Equal(t, w.chatID, got[i].ChatID, "result[%d] ChatID", i)
					assert.Equal(t, w.msgID, got[i].TargetMessageID, "result[%d] TargetMessageID", i)
				}
			}
		})
	}
}
