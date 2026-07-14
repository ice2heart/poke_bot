package main

import (
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
)

func TestBuildStoredText(t *testing.T) {
	tests := []struct {
		name string
		msg  *models.Message
		want string
	}{
		{
			name: "plain text",
			msg:  &models.Message{Text: "hello"},
			want: "hello",
		},
		{
			name: "sticker",
			msg:  &models.Message{Sticker: &models.Sticker{Emoji: "😀", SetName: "funpack"}},
			want: "Sticker: 😀, pack: funpack",
		},
		{
			name: "animation",
			msg:  &models.Message{Animation: &models.Animation{FileName: "cat.mp4"}},
			want: "GIF: name cat.mp4",
		},
		{
			name: "photo with caption",
			msg: &models.Message{
				Photo:   []models.PhotoSize{{FileID: "x"}},
				Caption: "look at this",
			},
			want: "Photo, text:\nlook at this",
		},
		{
			name: "hidden text-link URLs appended",
			msg: &models.Message{
				Text: "click here",
				Entities: []models.MessageEntity{
					{Type: models.MessageEntityTypeTextLink, URL: "https://example.com"},
				},
			},
			want: "click here\nhttps://example.com",
		},
		{
			name: "hidden URLs from caption entities appended",
			msg: &models.Message{
				Caption: "promo",
				CaptionEntities: []models.MessageEntity{
					{Type: models.MessageEntityTypeTextLink, URL: "https://spam.example"},
				},
			},
			want: "Photo, text:\npromo\nhttps://spam.example",
		},
		{
			name: "photo without text",
			msg:  &models.Message{Photo: []models.PhotoSize{{FileID: "x"}}},
			want: "A photo without text",
		},
		{
			name: "video without text",
			msg:  &models.Message{Video: &models.Video{FileID: "v"}},
			want: "A video without text",
		},
		{
			name: "empty message",
			msg:  &models.Message{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, buildStoredText(tt.msg))
		})
	}
}

func TestCollectHiddenURLs(t *testing.T) {
	tests := []struct {
		name         string
		entitySlices [][]models.MessageEntity
		want         []string
	}{
		{
			name: "no entities",
			want: nil,
		},
		{
			name: "text-link entity collected",
			entitySlices: [][]models.MessageEntity{
				{{Type: models.MessageEntityTypeTextLink, URL: "https://example.com"}},
			},
			want: []string{"https://example.com"},
		},
		{
			name: "plain URL entity has no URL field and is skipped",
			entitySlices: [][]models.MessageEntity{
				{{Type: models.MessageEntityTypeURL}},
			},
			want: nil,
		},
		{
			name: "multiple slices merged in order",
			entitySlices: [][]models.MessageEntity{
				{{Type: models.MessageEntityTypeTextLink, URL: "https://a.example"}},
				{
					{Type: models.MessageEntityTypeBold},
					{Type: models.MessageEntityTypeTextLink, URL: "https://b.example"},
				},
			},
			want: []string{"https://a.example", "https://b.example"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, collectHiddenURLs(tt.entitySlices...))
		})
	}
}

func TestExtractLinkedMessageIDs(t *testing.T) {
	const (
		chatID       = int64(-1001234567890)
		chatUsername = "mychat"
	)

	urlEntity := func(text string) []models.MessageEntity {
		return []models.MessageEntity{
			{Type: models.MessageEntityTypeURL, Offset: 0, Length: len(text)},
		}
	}

	tests := []struct {
		name     string
		text     string
		entities []models.MessageEntity
		want     []int
	}{
		{
			name:     "public link with matching username",
			text:     "https://t.me/mychat/42",
			entities: urlEntity("https://t.me/mychat/42"),
			want:     []int{42},
		},
		{
			name:     "public link with wrong username",
			text:     "https://t.me/otherchat/42",
			entities: urlEntity("https://t.me/otherchat/42"),
			want:     nil,
		},
		{
			name:     "private link with matching chat ID",
			text:     "https://t.me/c/1234567890/17",
			entities: urlEntity("https://t.me/c/1234567890/17"),
			want:     []int{17},
		},
		{
			name:     "private link with wrong chat ID",
			text:     "https://t.me/c/9999999999/17",
			entities: urlEntity("https://t.me/c/9999999999/17"),
			want:     nil,
		},
		{
			name: "non-URL entities are ignored",
			text: "https://t.me/mychat/42",
			entities: []models.MessageEntity{
				{Type: models.MessageEntityTypeBold, Offset: 0, Length: 22},
			},
			want: nil,
		},
		{
			name: "multiple URL entities collected",
			text: "https://t.me/mychat/1 https://t.me/mychat/2",
			entities: []models.MessageEntity{
				{Type: models.MessageEntityTypeURL, Offset: 0, Length: 21},
				{Type: models.MessageEntityTypeURL, Offset: 22, Length: 21},
			},
			want: []int{1, 2},
		},
		{
			name:     "no entities",
			text:     "https://t.me/mychat/42",
			entities: nil,
			want:     nil,
		},
		{
			name:     "telegram.me public link with matching username",
			text:     "https://telegram.me/mychat/42",
			entities: urlEntity("https://telegram.me/mychat/42"),
			want:     []int{42},
		},
		{
			name:     "telegram.me private link with matching chat ID",
			text:     "https://telegram.me/c/1234567890/17",
			entities: urlEntity("https://telegram.me/c/1234567890/17"),
			want:     []int{17},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractLinkedMessageIDs(tt.entities, tt.text, chatID, chatUsername))
		})
	}
}
