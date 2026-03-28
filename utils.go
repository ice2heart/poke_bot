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

func parseChatLink(link string, chatID int64, chatName string) (parsedLinks []ParsedLink) {
	rxResult := linkRegex.FindAllStringSubmatch(link, -1)
	parsedLinks = []ParsedLink{}
	var err error
	for i := range rxResult {
		parsedLink := ParsedLink{err: nil}
		pokeMessageID := int64(0)
		if rxResult[i][1] == "" {
			linkUsername := rxResult[i][2]
			if linkUsername != chatName {
				parsedLink.err = fmt.Errorf("Chat Username is not match %s != %s\n", linkUsername, chatName)
				goto Append
			}
		} else {
			parsedID, _ := strconv.ParseInt("-100"+rxResult[i][2], 10, 64)
			if chatID != parsedID {
				parsedLink.err = fmt.Errorf("Chat ID is not match %d != %d\n", parsedID, chatID)
				goto Append
			}
		}
		pokeMessageID, err = strconv.ParseInt(rxResult[i][3], 10, 64)
		if err != nil {
			parsedLink.err = err
			goto Append
		}

		parsedLink.TargetMessageID = pokeMessageID
		parsedLink.ChatID = chatID
	Append:
		parsedLinks = append(parsedLinks, parsedLink)
	}
	return
}
