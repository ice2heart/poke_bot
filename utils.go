package main

import (
	"fmt"
	"strconv"
)

func firstN(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
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
