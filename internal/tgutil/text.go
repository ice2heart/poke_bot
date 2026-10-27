package tgutil

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var linkRegex = regexp.MustCompile(`(?:\s*https://(?:t\.me|telegram\.me)/(c/)?([\d\w]+)/(\d+)(?:\?comment=(\d+))?)`)

// MatchMessageLinks returns the submatches of every Telegram message link in s:
// [full, "c/" for private chats, chat username or internal ID, message ID, comment ID].
func MatchMessageLinks(s string) [][]string {
	return linkRegex.FindAllStringSubmatch(s, -1)
}

// Do not touch, this is because UTF-8 symbols != bytes
func FirstN(s string, n int) string {
	if len(s) < n {
		return s
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

// entityText extracts the substring for a Telegram message entity.
// Telegram offsets and lengths are in UTF-16 code units; Go strings are UTF-8,
// so naive byte slicing corrupts multi-byte characters.
func EntityText(s string, offset, length int) string {
	byteStart := -1
	byteEnd := -1
	u16 := 0
	for i, r := range s {
		if u16 == offset {
			byteStart = i
		}
		if u16 == offset+length {
			byteEnd = i
			break
		}
		if r >= 0x10000 {
			u16 += 2 // surrogate pair in UTF-16
		} else {
			u16++
		}
	}
	if byteStart == -1 {
		return ""
	}
	if byteEnd == -1 {
		return s[byteStart:]
	}
	return s[byteStart:byteEnd]
}

type ParsedLink struct {
	ChatID          int64
	TargetMessageID int64
	Err             error
}

func ParseChatLink(link string, chatID int64, chatName string, linkedChannelUsername string) (parsedLinks []ParsedLink) {
	parsedLinks = []ParsedLink{}
	for _, m := range MatchMessageLinks(link) {
		parsedLink := ParsedLink{}

		// Channel comment link: https://t.me/<channel>/postID?comment=msgID
		// The comment value is the message ID in the linked discussion group.
		if linkedChannelUsername != "" && m[1] == "" && m[2] == linkedChannelUsername && m[4] != "" {
			msgID, err := strconv.ParseInt(m[4], 10, 64)
			if err != nil {
				parsedLink.Err = fmt.Errorf("invalid comment ID %q: %w", m[4], err)
			} else {
				parsedLink.TargetMessageID = msgID
				parsedLink.ChatID = chatID
			}
			parsedLinks = append(parsedLinks, parsedLink)
			continue
		}

		// Standard direct-message link.
		if m[1] == "" {
			if m[2] != chatName {
				parsedLink.Err = fmt.Errorf("chat username mismatch: got %q want %q", m[2], chatName)
				parsedLinks = append(parsedLinks, parsedLink)
				continue
			}
		} else {
			parsedID, _ := strconv.ParseInt("-100"+m[2], 10, 64)
			if chatID != parsedID {
				parsedLink.Err = fmt.Errorf("chat ID mismatch: got %d want %d", parsedID, chatID)
				parsedLinks = append(parsedLinks, parsedLink)
				continue
			}
		}
		msgID, err := strconv.ParseInt(m[3], 10, 64)
		if err != nil {
			parsedLink.Err = err
		} else {
			parsedLink.TargetMessageID = msgID
			parsedLink.ChatID = chatID
		}
		parsedLinks = append(parsedLinks, parsedLink)
	}
	return
}

var (
	publicGroupRX = regexp.MustCompile(`^-100`)
	mdRegex       = regexp.MustCompile(`(['_~>#!=\-])`)
)

// QuoteText escapes text and formats each line as a Telegram block-quote (">line").
func QuoteText(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = ">" + Escape(line)
	}
	return strings.Join(lines, "\n")
}

func Escape(line string) string {
	first_step := regexp.QuoteMeta(line)
	second_step := strings.ReplaceAll(first_step, "`", "\\`")
	return mdRegex.ReplaceAllString(second_step, "\\$1")
}

func MakePublicGroupString(groupID int64) string {
	// https://gist.github.com/nafiesl/4ad622f344cd1dc3bb1ecbe468ff9f8a
	// groupID has leading -100
	return publicGroupRX.ReplaceAllString(strconv.FormatInt(groupID, 10), "")
}
