package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCccpRegex(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		// Latin spelling.
		{"latin lower", "/mute^cccp", true},
		{"latin upper", "/mute^CCCP", true},
		{"latin mixed case", "/mute^CcCp", true},
		{"latin with link", "/mute^cccp https://t.me/c/1657123097/2854347", true},
		{"latin with mention", "/mute^cccp @someuser", true},

		// Cyrillic spelling.
		{"cyrillic lower", "/mute^ссср", true},
		{"cyrillic upper", "/mute^СССР", true},
		{"cyrillic with link", "/mute^ссср https://t.me/c/1/2", true},

		// Telegram appends @botusername in groups; the marker may sit either side.
		{"botname then marker", "/mute@poke_bot^cccp", true},
		{"botname then cyrillic marker", "/mute@poke_bot^ссср", true},
		{"marker then botname", "/mute^cccp@poke_bot", true},
		{"botname then marker with link", "/mute@poke_bot^cccp https://t.me/c/1/2", true},

		// /voteeblan is registered to the same handler closure.
		{"voteeblan latin", "/voteeblan^cccp @someuser", true},
		{"voteeblan cyrillic", "/voteeblan^ссср", true},

		// Plain commands must stay plain.
		{"plain mute", "/mute", false},
		{"plain mute with link", "/mute https://t.me/c/1657123097/2854347", false},
		{"plain mute with mention", "/mute @someuser", false},
		{"mute with botname", "/mute@poke_bot", false},
		{"mute with botname and link", "/mute@poke_bot https://t.me/c/1/2", false},
		{"voteeblan plain", "/voteeblan @someuser", false},
		{"empty", "", false},

		// The reason the marker is '^' and not '@': @cccp is a claimable
		// username, so an '@' marker would fire while muting that very user.
		{"username collision mention", "/mute @cccp", false},
		{"username collision cyrillic", "/mute @ссср", false},
		{"username collision in link", "/mute https://t.me/cccp/1", false},

		// Guards against two ways the pattern could silently break.
		// Bare "ссср" with no caret: `\^(cccp)|(ссср)` would have matched this,
		// because | binds looser than the group.
		{"bare cyrillic word", "ссср", false},
		{"cyrillic word in sentence", "вот это ссср такой", false},
		// Plain "cccp" with no caret: catches an unescaped '^' in the pattern.
		{"bare latin word", "look at cccp", false},

		// Near misses.
		{"wrong marker word", "/mute^ussr", false},
		{"caret alone", "/mute^", false},
		{"too short", "/mute^ccp", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cccpRegex.MatchString(tt.text), "text=%q", tt.text)
		})
	}
}
