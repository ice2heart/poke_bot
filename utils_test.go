package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
