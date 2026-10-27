package storage

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// DB schema
// uid - index int64
// counter - inc counter
// voteCounter - inc counter

const (
	VOTE_RATING_MULTIPLY  = 10
	CUSTOM_TAG_MIN_RATING = 200
)

type UserRecord struct {
	ID          primitive.ObjectID `bson:"_id"`
	Uid         int64
	Counter     uint32
	VoteCounter uint32
	Username    string
	AltUsername string
	MuteCounter int
	CustomTag   string
}

type ChatMessage struct {
	MessageID int64
	ChatID    int64
	UserID    int64
	UserName  string
	Text      string
	Date      uint64
}

type ReactionRecord struct {
	UserID    int64  `bson:"userid"`
	ChatID    int64  `bson:"chatid"`
	MessageID int    `bson:"messageid"`
	Emoji     string `bson:"emoji"`
	Date      int64  `bson:"date"`
}

type ScoreResult struct {
	Rating int   `bson:"rating"`
	Userid int64 `bson:"userid"`
}

type DynamicSetting struct {
	ChatID                int64
	Pause                 bool
	LogRecipients         []int64
	ChatName              string
	ChatUsername          string
	ChatAccessHash        int64
	LinkedChannelUsername string
	// BanPatterns are case-insensitive regexps; a message matching any of them
	// automatically starts a ban vote against its author.
	BanPatterns []string
	// FileIDs caches the Telegram file_id of each embedded image, keyed by its
	// registry name (see images in embedded_images.go). A file_id is valid for
	// the bot in every chat, so these are logically global — they live here to
	// reuse the settings plumbing, and whichever chat recorded one serves all.
	FileIDs map[string]string
}

// BanLogRecord is the persisted shape of a finished vote in the ban_log
// collection. Field names are lowercased by the bson codec, so they must not
// change.
type BanLogRecord struct {
	ChatID           int64
	TargetMessageID  int64
	UserID           int64
	Score            int16
	OwnerID          int64
	RequestMessageID int64
	VoteMessageID    int64
	UserName         string
	ProfileName      string
	LastMessage      string
	BanMessage       string
	Voters           map[int64]int8
	Type             uint8
	CreatedAt        time.Time
	Cccp             bool
}
