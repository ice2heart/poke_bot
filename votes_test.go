package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTallyVotes(t *testing.T) {
	tests := []struct {
		name          string
		voters        map[int64]int8
		wantUpvotes   int
		wantDownvotes int
	}{
		{
			name:   "no voters",
			voters: map[int64]int8{},
		},
		{
			name:        "all upvotes",
			voters:      map[int64]int8{1: 1, 2: 1, 3: 1},
			wantUpvotes: 3,
		},
		{
			name:          "all downvotes",
			voters:        map[int64]int8{1: -1, 2: -1},
			wantDownvotes: 2,
		},
		{
			name:          "mixed votes",
			voters:        map[int64]int8{1: 1, 2: -1, 3: 1, 4: -1, 5: 1},
			wantUpvotes:   3,
			wantDownvotes: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upvotes, downvotes := tallyVotes(tt.voters)
			assert.Equal(t, tt.wantUpvotes, upvotes, "upvotes")
			assert.Equal(t, tt.wantDownvotes, downvotes, "downvotes")
		})
	}
}

func TestVoteVerdict(t *testing.T) {
	tests := []struct {
		name      string
		upvotes   int
		downvotes int
		superPoke int
		score     int16
		want      int
	}{
		{
			name:      "admin super upvote wins immediately",
			superPoke: 1,
			score:     HIGH_SCORE,
			want:      1,
		},
		{
			name:      "admin super downvote cancels immediately",
			downvotes: 0,
			superPoke: -1,
			score:     LOW_SCORE,
			want:      -1,
		},
		{
			name:    "upvote margin reaches required score",
			upvotes: 3,
			score:   LOW_SCORE,
			want:    1,
		},
		{
			name:      "upvote margin counts net votes",
			upvotes:   4,
			downvotes: 2,
			score:     LOW_SCORE,
			want:      0,
		},
		{
			name:      "downvote margin reaches cancel threshold",
			upvotes:   1,
			downvotes: 1 + int(MID_SCORE),
			score:     HIGH_SCORE,
			want:      -1,
		},
		{
			name:    "not enough votes yet",
			upvotes: 1,
			score:   MID_SCORE,
			want:    0,
		},
		{
			name:  "no votes",
			score: LOW_SCORE,
			want:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, voteVerdict(tt.upvotes, tt.downvotes, tt.superPoke, tt.score))
		})
	}
}

func TestUserTag(t *testing.T) {
	assert.Equal(t, "@someuser", userTag("someuser", "", 0))
	assert.Equal(t, "@some\\_user", userTag("some_user", "", 0))
	assert.Equal(t, "[John Smith](tg://user?id=42)", userTag("", "John Smith ", 42))
}

func TestFormatVotersReport(t *testing.T) {
	tagFor := func(id int64) string { return fmt.Sprintf("u%d", id) }

	tests := []struct {
		name    string
		banInfo *BanInfo
		want    string
	}{
		{
			name: "mixed votes sorted by user ID",
			banInfo: &BanInfo{
				UserName: "spammer",
				Voters:   map[int64]int8{10: 1, 5: 1, 7: -1},
			},
			want: "Голосование по @spammer\nЗа \\(2\\):\nu5\nu10\nПротив \\(1\\):\nu7",
		},
		{
			name: "only upvotes",
			banInfo: &BanInfo{
				UserName: "spammer",
				Voters:   map[int64]int8{1: 1},
			},
			want: "Голосование по @spammer\nЗа \\(1\\):\nu1",
		},
		{
			name: "no voters recorded",
			banInfo: &BanInfo{
				UserName: "spammer",
			},
			want: "Голосование по @spammer\nГолосов не зафиксировано",
		},
		{
			name: "target without username uses profile link",
			banInfo: &BanInfo{
				ProfileName: "John Smith",
				UserID:      42,
				Voters:      map[int64]int8{3: -1},
			},
			want: "Голосование по [John Smith](tg://user?id=42)\nПротив \\(1\\):\nu3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatVotersReport(tt.banInfo, tagFor))
		})
	}
}

func TestVoteExpiredText(t *testing.T) {
	tests := []struct {
		name        string
		userName    string
		profileName string
		userID      int64
		want        string
	}{
		{
			name:     "with username",
			userName: "someuser",
			want:     "Голосование истекло — необходимое количество голосов не набрано\\. @someuser",
		},
		{
			name:     "username with markdown special chars is escaped",
			userName: "some_user",
			want:     "Голосование истекло — необходимое количество голосов не набрано\\. @some\\_user",
		},
		{
			name:        "without username falls back to profile name link",
			profileName: "John Smith",
			userID:      42,
			want:        "Голосование истекло — необходимое количество голосов не набрано\\. [John Smith](tg://user?id=42)",
		},
		{
			name:        "profile name is trimmed",
			profileName: "John ",
			userID:      7,
			want:        "Голосование истекло — необходимое количество голосов не набрано\\. [John](tg://user?id=7)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, voteExpiredText(tt.userName, tt.profileName, tt.userID))
		})
	}
}
