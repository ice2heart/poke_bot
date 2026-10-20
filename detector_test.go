package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsePatternArg(t *testing.T) {
	tests := []struct {
		name  string
		arg   string
		want  string
		match []string
		miss  []string
	}{
		{
			name:  "quoted phrase is literal",
			arg:   `"Если кому интересно пишите в личку, отправлю."`,
			want:  `Если\s+кому\s+интересно\s+пишите\s+в\s+личку,\s+отправлю\.`,
			match: []string{"Если кому интересно пишите в личку, отправлю.", "привет! если кому  интересно пишите в\nличку, отправлю."},
			miss:  []string{"Если кому интересно пишите в личку, отправлю!"},
		},
		{
			name:  "guillemets",
			arg:   "«a+b (c)»",
			want:  `a\+b\s+\(c\)`,
			match: []string{"x a+b (c) y"},
			miss:  []string{"aab c"},
		},
		{
			name:  "curly quotes",
			arg:   "“пишите в личку”",
			match: []string{"пишите в личку"},
		},
		{
			name:  "unquoted is regexp",
			arg:   "  пиш[иу]те в лс ",
			want:  "пиш[иу]те в лс",
			match: []string{"Пишуте в лс"},
		},
		{name: "unbalanced quote stays regexp", arg: `"abc`, want: `"abc`},
		{name: "empty quotes stay as is", arg: `""`, want: `""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePatternArg(tt.arg)
			if tt.want != "" {
				assert.Equal(t, tt.want, got)
			}
			re := compiledPattern(got)
			if !assert.NotNil(t, re) {
				return
			}
			for _, m := range tt.match {
				assert.True(t, re.MatchString(m), "should match %q", m)
			}
			for _, m := range tt.miss {
				assert.False(t, re.MatchString(m), "should not match %q", m)
			}
		})
	}
}
