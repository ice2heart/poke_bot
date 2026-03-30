package main

import (
	"fmt"
	"strconv"
)

// Do not touch, this is because UTF-8 symbols != bytes
func firstN(s string, n int) string {
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
func entityText(s string, offset, length int) string {
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
	err             error
}

func parseChatLink(link string, chatID int64, chatName string, linkedChannelUsername string) (parsedLinks []ParsedLink) {
	parsedLinks = []ParsedLink{}
	for _, m := range linkRegex.FindAllStringSubmatch(link, -1) {
		parsedLink := ParsedLink{}

		// Channel comment link: https://t.me/<channel>/postID?comment=msgID
		// The comment value is the message ID in the linked discussion group.
		if linkedChannelUsername != "" && m[1] == "" && m[2] == linkedChannelUsername && m[4] != "" {
			msgID, err := strconv.ParseInt(m[4], 10, 64)
			if err != nil {
				parsedLink.err = fmt.Errorf("invalid comment ID %q: %w", m[4], err)
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
				parsedLink.err = fmt.Errorf("chat username mismatch: got %q want %q", m[2], chatName)
				parsedLinks = append(parsedLinks, parsedLink)
				continue
			}
		} else {
			parsedID, _ := strconv.ParseInt("-100"+m[2], 10, 64)
			if chatID != parsedID {
				parsedLink.err = fmt.Errorf("chat ID mismatch: got %d want %d", parsedID, chatID)
				parsedLinks = append(parsedLinks, parsedLink)
				continue
			}
		}
		msgID, err := strconv.ParseInt(m[3], 10, 64)
		if err != nil {
			parsedLink.err = err
		} else {
			parsedLink.TargetMessageID = msgID
			parsedLink.ChatID = chatID
		}
		parsedLinks = append(parsedLinks, parsedLink)
	}
	return
}
