package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetMuteDurationText(t *testing.T) {
	tests := []struct {
		name     string
		muteDays int
		want     string
	}{
		{
			name:     "test1",
			muteDays: 1,
			want:     "сутки",
		},
		{
			name:     "test2",
			muteDays: 2,
			want:     "2 дня",
		},
		{
			name:     "test3",
			muteDays: 3,
			want:     "3 дня",
		},
		{
			name:     "test4",
			muteDays: 4,
			want:     "4 дня",
		},
		{
			name:     "test5",
			muteDays: 5,
			want:     "5 дней",
		},
		{
			name:     "test6",
			muteDays: 6,
			want:     "6 дней",
		},
		{
			name:     "test7",
			muteDays: 7,
			want:     "7 дней",
		},
		{
			name:     "test8",
			muteDays: 8,
			want:     "8 дней",
		},
		{
			name:     "test9",
			muteDays: 9,
			want:     "9 дней",
		},
		{
			name:     "test10",
			muteDays: 10,
			want:     "10 дней",
		},
		{
			name:     "test11",
			muteDays: 11,
			want:     "11 дней",
		},
		{
			name:     "test12",
			muteDays: 12,
			want:     "12 дней",
		},
		{
			name:     "test13",
			muteDays: 13,
			want:     "13 дней",
		},
		{
			name:     "test14",
			muteDays: 14,
			want:     "14 дней",
		},
		{
			name:     "test15",
			muteDays: 15,
			want:     "15 дней",
		},
		{
			name:     "test16",
			muteDays: 16,
			want:     "16 дней",
		},
		{
			name:     "test17",
			muteDays: 17,
			want:     "17 дней",
		},
		{
			name:     "test18",
			muteDays: 18,
			want:     "18 дней",
		},
		{
			name:     "test19",
			muteDays: 19,
			want:     "19 дней",
		},
		{
			name:     "test20",
			muteDays: 20,
			want:     "20 дней",
		},
		{
			name:     "test21",
			muteDays: 21,
			want:     "21 день",
		},
		{
			name:     "test22",
			muteDays: 22,
			want:     "22 дня",
		},
		{
			name:     "test23",
			muteDays: 23,
			want:     "23 дня",
		},
		{
			name:     "test24",
			muteDays: 24,
			want:     "24 дня",
		},
		{
			name:     "test25",
			muteDays: 25,
			want:     "25 дней",
		},
		{
			name:     "test26",
			muteDays: 26,
			want:     "26 дней",
		},
		{
			name:     "test27",
			muteDays: 27,
			want:     "27 дней",
		},
		{
			name:     "test28",
			muteDays: 28,
			want:     "28 дней",
		},
		{
			name:     "test29",
			muteDays: 29,
			want:     "29 дней",
		},
		{
			name:     "test30",
			muteDays: 30,
			want:     "30 дней",
		},
		{
			name:     "test31",
			muteDays: 31,
			want:     "31 день",
		},
		{
			name:     "test32",
			muteDays: 32,
			want:     "32 дня",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getMuteDurationTextFromDays(tt.muteDays); !assert.Equal(t, got, tt.want) {
				t.Errorf("getMuteDurationText() = %v, want %v", got, tt.want)
			}
		})
	}
}
