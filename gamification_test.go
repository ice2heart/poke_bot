package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatCheckReport(t *testing.T) {
	tests := []struct {
		name     string
		user     *UserRecord
		messages []ChatMessage
		want     string
	}{
		{
			name: "user with username and no messages",
			user: &UserRecord{Uid: 42, Username: "bob", Counter: 100, VoteCounter: 2},
			want: "@bob\nРейтинг: 120 \\(сообщений: 100, фрагов: 2\\)",
		},
		{
			name: "user without username falls back to profile link",
			user: &UserRecord{Uid: 42, AltUsername: "John Smith", Counter: 5},
			want: "[John Smith](tg://user?id=42)\nРейтинг: 5 \\(сообщений: 5, фрагов: 0\\)",
		},
		{
			name: "mute counter shown when non-zero",
			user: &UserRecord{Uid: 42, Username: "bob", Counter: 1, MuteCounter: 3},
			want: "@bob\nРейтинг: 1 \\(сообщений: 1, фрагов: 0\\)\nМутов: 3",
		},
		{
			name: "messages quoted newest first",
			user: &UserRecord{Uid: 42, Username: "bob", Counter: 10},
			messages: []ChatMessage{
				{Text: "second message"},
				{Text: "first_message"},
			},
			want: "@bob\nРейтинг: 10 \\(сообщений: 10, фрагов: 0\\)\nПоследние сообщения:\n>second message\n>first\\_message",
		},
		{
			name: "multiline message quoted per line",
			user: &UserRecord{Uid: 42, Username: "bob", Counter: 10},
			messages: []ChatMessage{
				{Text: "line one\nline two"},
			},
			want: "@bob\nРейтинг: 10 \\(сообщений: 10, фрагов: 0\\)\nПоследние сообщения:\n>line one\n>line two",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatCheckReport(tt.user, tt.messages))
		})
	}
}
