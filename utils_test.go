package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
