package main

import (
	"encoding/base64"
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

// pack more compact
type Item struct {
	Action uint8                 `msgpack:"a"`
	ChatID int64                 `msgpack:"c"`
	Data   map[uint8]interface{} `msgpack:"d,omitempty"`
}

const (
	ACTION_UNBAN      uint8 = 1
	ACTION_DELETE_ALL uint8 = 2
	// admin menu
	ACTION_SHOW_CHAT_LIST uint8 = 3
	ACTION_SHOW_CHAT_ID   uint8 = 4
	ACTION_PAUSE_CHAT     uint8 = 5
	ACTION_UNPAUSE_CHAT   uint8 = 6
	ACTION_ENABLED_LOG    uint8 = 7
	ACTION_DISABLED_LOG   uint8 = 8
)

const (
	DATA_TYPE_USERID uint8 = 1
)

func getInt(data any) int64 {
	switch v := data.(type) {
	case int:
		return int64(v)
	case uint:
		return int64(v)
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return int64(v)
	case uint8:
		return int64(v)
	case uint16:
		return int64(v)
	case uint32:
		return int64(v)
	default:
		fmt.Println("Not found")
	}
	return 0
}

func marshal(data *Item) (output string, err error) {
	b, err := msgpack.Marshal(data)
	if err != nil {
		return
	}
	output = base64.StdEncoding.EncodeToString(b)
	return output, nil
}

func unmarshal(input string) (data *Item, err error) {
	dec, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		return nil, err
	}
	var item Item
	err = msgpack.Unmarshal(dec, &item)
	if err != nil {
		return nil, err
	}
	return &item, nil
}
