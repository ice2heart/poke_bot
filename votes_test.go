package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestVoteTypesCoverEveryType(t *testing.T) {
	// Every vote type must have a table entry: castVote and getVoteButtons both
	// fall back to an error path for a type that is missing one.
	for _, voteType := range []uint8{BAN, MUTE, TEXT_ONLY} {
		vt, ok := voteTypes[voteType]
		require.True(t, ok, "vote type %d has no voteTypes entry", voteType)
		assert.NotEmpty(t, vt.upText, "type %d upText", voteType)
		assert.NotEmpty(t, vt.downText, "type %d downText", voteType)
		assert.NotEmpty(t, vt.upAnswer, "type %d upAnswer", voteType)
		assert.NotEmpty(t, vt.downAnswer, "type %d downAnswer", voteType)
		assert.NotNil(t, vt.apply, "type %d apply", voteType)
	}
}

func TestCastVote(t *testing.T) {
	const (
		chatID = int64(-1001234567890)
		msgID  = int64(555)
		owner  = int64(1)
		voter  = int64(2)
	)

	// newSession builds a running vote owned by owner, with the given votes
	// already recorded, registered in a chat session map.
	newSession := func(voteType uint8, score int16, voters map[int64]int8) (*BanInfo, map[int64]*BanInfo) {
		s := &BanInfo{
			ChatID:  chatID,
			UserID:  99,
			OwnerID: owner,
			Type:    voteType,
			Score:   score,
			Voters:  voters,
		}
		return s, map[int64]*BanInfo{msgID: s}
	}

	// The action closures perform Telegram I/O, so these cases only assert on the
	// session state and the answer — never invoking result.action.

	t.Run("an upvote is recorded with the vote type's answer", func(t *testing.T) {
		for voteType, vt := range voteTypes {
			s, chatSession := newSession(voteType, HIGH_SCORE, map[int64]int8{})

			result := castVote(t.Context(), nil, s, chatSession, msgID, voter, VOTE_UP, 0, true)

			assert.Equal(t, vt.upAnswer, result.answer, "type %d", voteType)
			assert.False(t, result.decided, "type %d", voteType)
			assert.NotNil(t, result.action, "type %d: an undecided vote refreshes its buttons", voteType)
			assert.Equal(t, map[int64]int8{voter: VOTE_UP}, s.Voters, "type %d", voteType)
			assert.Contains(t, chatSession, msgID, "type %d: an undecided vote stays running", voteType)
		}
	})

	t.Run("a downvote is recorded with the vote type's answer", func(t *testing.T) {
		for voteType, vt := range voteTypes {
			s, chatSession := newSession(voteType, HIGH_SCORE, map[int64]int8{})

			result := castVote(t.Context(), nil, s, chatSession, msgID, voter, VOTE_DOWN, 0, true)

			assert.Equal(t, vt.downAnswer, result.answer, "type %d", voteType)
			assert.Equal(t, map[int64]int8{voter: VOTE_DOWN}, s.Voters, "type %d", voteType)
		}
	})

	t.Run("the owner cannot vote on their own poll", func(t *testing.T) {
		s, chatSession := newSession(BAN, HIGH_SCORE, map[int64]int8{})

		result := castVote(t.Context(), nil, s, chatSession, msgID, owner, VOTE_UP, 0, true)

		assert.Equal(t, ANSWER_OWN, result.answer)
		assert.Nil(t, result.action)
		assert.Empty(t, s.Voters)
	})

	t.Run("an admin superPoke overrides the owner guard", func(t *testing.T) {
		// The owner cancelling their own vote arrives as a super downvote.
		s, chatSession := newSession(BAN, HIGH_SCORE, map[int64]int8{})

		result := castVote(t.Context(), nil, s, chatSession, msgID, owner, VOTE_DOWN, -1, true)

		assert.True(t, result.decided)
		assert.NotContains(t, chatSession, msgID)
	})

	t.Run("replace lets a voter change their mind", func(t *testing.T) {
		s, chatSession := newSession(BAN, HIGH_SCORE, map[int64]int8{voter: VOTE_UP})

		result := castVote(t.Context(), nil, s, chatSession, msgID, voter, VOTE_DOWN, 0, true)

		assert.Equal(t, voteTypes[BAN].downAnswer, result.answer)
		assert.Equal(t, map[int64]int8{voter: VOTE_DOWN}, s.Voters)
	})

	t.Run("without replace an existing vote is not counted twice", func(t *testing.T) {
		s, chatSession := newSession(BAN, HIGH_SCORE, map[int64]int8{voter: VOTE_UP})

		result := castVote(t.Context(), nil, s, chatSession, msgID, voter, VOTE_UP, 0, false)

		assert.Equal(t, ANSWER_COUNTED, result.answer)
		assert.Nil(t, result.action)
		assert.Equal(t, map[int64]int8{voter: VOTE_UP}, s.Voters)
	})

	t.Run("without replace an existing downvote is not flipped", func(t *testing.T) {
		s, chatSession := newSession(BAN, HIGH_SCORE, map[int64]int8{voter: VOTE_DOWN})

		result := castVote(t.Context(), nil, s, chatSession, msgID, voter, VOTE_UP, 0, false)

		assert.Equal(t, ANSWER_COUNTED, result.answer)
		assert.Nil(t, result.action)
		assert.Equal(t, map[int64]int8{voter: VOTE_DOWN}, s.Voters,
			"a repeated command must not flip a downvote into an upvote")
	})

	t.Run("the deciding upvote settles the session", func(t *testing.T) {
		// LOW_SCORE is 3, reached by this single added upvote.
		s, chatSession := newSession(BAN, LOW_SCORE, map[int64]int8{3: VOTE_UP, 4: VOTE_UP})

		result := castVote(t.Context(), nil, s, chatSession, msgID, voter, VOTE_UP, 0, false)

		assert.Equal(t, voteTypes[BAN].upAnswer, result.answer)
		assert.True(t, result.decided)
		assert.NotNil(t, result.action)
		assert.NotContains(t, chatSession, msgID, "a decided vote is claimed under the lock")
	})

	t.Run("an unknown vote type is refused", func(t *testing.T) {
		s, chatSession := newSession(240, LOW_SCORE, map[int64]int8{})

		result := castVote(t.Context(), nil, s, chatSession, msgID, voter, VOTE_UP, 0, true)

		assert.Equal(t, ANSWER_SOMETHING_WRONG, result.answer)
		assert.Nil(t, result.action)
		assert.Empty(t, s.Voters)
	})
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
