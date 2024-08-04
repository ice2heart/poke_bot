package main

import (
	"testing"
)

func TestGetMuteDurationText(t *testing.T) {
	tests := []struct {
		name string
		user UserRecord
		want string
	}{
		{
			name: "test1",
			user: UserRecord{MuteCounter: 0},
			want: "1 день",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getMuteDurationText(tt.user); got != tt.want {
				t.Errorf("getMuteDurationText() = %v, want %v", got, tt.want)
			}
		})
	}
}
