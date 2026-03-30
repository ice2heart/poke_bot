package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.uber.org/zap"
)

var (
	// client   *mongo.Client
	dataBase *mongo.Database

	usersCollection        *mongo.Collection
	banLogs                *mongo.Collection
	chatMessages           *mongo.Collection
	chatSettingsCollection *mongo.Collection
	reactionsCollection    *mongo.Collection

	upsertOptions *options.UpdateOptions
)

// DB schema
// uid - index int64
// counter - inc counter
// voteCounter - inc counter

const (
	VOTE_RATING_MULTIPLY = 10
)

type UserRecord struct {
	ID          primitive.ObjectID `bson:"_id"`
	Uid         int64
	Counter     uint32
	VoteCounter uint32
	Username    string
	AltUsername string
	MuteCounter int
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
}

func initDb(ctx context.Context, connectionLine string, dbName string) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(connectionLine))
	if err != nil {
		panic(err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		panic(err)
	}
	zap.S().Infof("[initDb] MongoDB connected, database=%q", dbName)
	upsertOptions = &options.UpdateOptions{}
	upsertOptions.SetUpsert(true)
	dataBase = client.Database(dbName)
	usersCollection = dataBase.Collection("users")
	banLogs = dataBase.Collection("ban_log")
	chatMessages = dataBase.Collection("messages")
	chatSettingsCollection = dataBase.Collection("settings")
	reactionsCollection = dataBase.Collection("reactions")
	ensureIndexes(ctx)
}

func ensureIndexes(ctx context.Context) {
	t := true

	// users: uid (unique) — primary lookup key
	if _, err := usersCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "uid", Value: 1}},
		Options: &options.IndexOptions{Unique: &t},
	}); err != nil {
		zap.S().Infof("[ensureIndexes] users.uid index: %v", err)
	}

	// users: username (sparse) — lookup by username
	sparse := true
	if _, err := usersCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "username", Value: 1}},
		Options: &options.IndexOptions{Sparse: &sparse},
	}); err != nil {
		zap.S().Infof("[ensureIndexes] users.username index: %v", err)
	}

	// messages: {chatid, messageid} — getMessageInfo / updateMessage
	if _, err := chatMessages.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "chatid", Value: 1}, {Key: "messageid", Value: 1}},
	}); err != nil {
		zap.S().Infof("[ensureIndexes] messages.{chatid,messageid} index: %v", err)
	}

	// messages: {chatid, userid, date} — getUserLastNthMessages (filter + sort)
	if _, err := chatMessages.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "chatid", Value: 1}, {Key: "userid", Value: 1}, {Key: "date", Value: -1}},
	}); err != nil {
		zap.S().Infof("[ensureIndexes] messages.{chatid,userid,date} index: %v", err)
	}

	// settings: chatid (unique) — one settings doc per chat
	if _, err := chatSettingsCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "chatid", Value: 1}},
		Options: &options.IndexOptions{Unique: &t},
	}); err != nil {
		zap.S().Infof("[ensureIndexes] settings.chatid index: %v", err)
	}

	// reactions: {chatid, userid} — lookup reactions per user per chat
	if _, err := reactionsCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "chatid", Value: 1}, {Key: "userid", Value: 1}},
	}); err != nil {
		zap.S().Infof("[ensureIndexes] reactions.{chatid,userid} index: %v", err)
	}

	// users: voteCounter descending — getTopUsersByVotes sort
	if _, err := usersCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "voteCounter", Value: -1}},
	}); err != nil {
		zap.S().Infof("[ensureIndexes] users.voteCounter index: %v", err)
	}

	zap.S().Info("[ensureIndexes] done")
}

// saveReaction inserts a reaction record into the reactions collection.
func saveReaction(ctx context.Context, rec *ReactionRecord) {
	if _, err := reactionsCollection.InsertOne(ctx, rec); err != nil {
		zap.S().Infof("[saveReaction] insert failed userID=%d chatID=%d emoji=%q: %v",
			rec.UserID, rec.ChatID, rec.Emoji, err)
	}
}

// ensureUser upserts a user record, setting uid/counters only on insert and
// updating username/altUsername whenever non-empty values are provided.
func ensureUser(ctx context.Context, userID int64, username, altUsername string) error {
	filter := bson.D{{Key: "uid", Value: userID}}
	setDoc := bson.D{}
	if username != "" {
		setDoc = append(setDoc, bson.E{Key: "username", Value: strings.ToLower(username)})
	}
	if altUsername != "" {
		setDoc = append(setDoc, bson.E{Key: "altUsername", Value: altUsername})
	}
	update := bson.D{
		{Key: "$setOnInsert", Value: bson.D{
			{Key: "uid", Value: userID},
			{Key: "counter", Value: 0},
			{Key: "voteCounter", Value: 0},
		}},
	}
	if len(setDoc) > 0 {
		update = append(update, bson.E{Key: "$set", Value: setDoc})
	}
	_, err := usersCollection.UpdateOne(ctx, filter, update, upsertOptions)
	return err
}

// getTopUsersByVotes returns up to limit users sorted by voteCounter descending.
func getTopUsersByVotes(ctx context.Context, limit int) ([]UserRecord, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: "voteCounter", Value: -1}}).
		SetLimit(int64(limit))
	cursor, err := usersCollection.Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, fmt.Errorf("getTopUsersByVotes: %w", err)
	}
	var users []UserRecord
	if err := cursor.All(ctx, &users); err != nil {
		return nil, fmt.Errorf("getTopUsersByVotes cursor.All: %w", err)
	}
	return users, nil
}

func userPlusOneMessage(ctx context.Context, uID int64, username string, altname string) {
	filter := bson.D{
		{Key: "uid", Value: uID},
	}
	update := bson.D{
		{Key: "$inc", Value: bson.D{
			{Key: "counter", Value: 1},
		}},
	}
	if len(username) != 0 {
		update = append(
			update,
			bson.E{
				Key:   "$set",
				Value: bson.D{{Key: "username", Value: strings.ToLower(username)}},
			},
		)
	}
	update = append(
		update,
		bson.E{
			Key:   "$set",
			Value: bson.D{{Key: "altUsername", Value: altname}},
		},
	)
	_, err := usersCollection.UpdateOne(ctx, filter, update, upsertOptions)
	if err != nil {
		zap.S().Infof("[userPlusOneMessage] upsert failed for userID=%d: %v", uID, err)
	}
}

func userAddMuteCounter(ctx context.Context, uID int64) error {
	filter := bson.D{
		{Key: "uid", Value: uID},
	}
	update := bson.D{
		{Key: "$inc", Value: bson.D{
			{Key: "muteCounter", Value: 1},
		}},
	}
	_, err := usersCollection.UpdateOne(ctx, filter, update, upsertOptions)
	return err
}

func userMakeVote(ctx context.Context, uID int64, amount int) {
	filter := bson.D{
		{Key: "uid", Value: uID},
	}
	update := bson.D{
		{Key: "$inc", Value: bson.D{
			{Key: "voteCounter", Value: amount},
		}},
	}
	_, err := usersCollection.UpdateOne(ctx, filter, update, upsertOptions)
	if err != nil {
		zap.S().Infof("[userMakeVote] upsert failed for userID=%d amount=%d: %v", uID, amount, err)
	}
}

func getRatingFromUserID(ctx context.Context, uID int64) (score *ScoreResult, err error) {
	filter := bson.D{
		{Key: "uid", Value: uID},
	}
	result := usersCollection.FindOne(ctx, filter)
	var user UserRecord
	err = result.Decode(&user)
	if err != nil {
		zap.S().Infof("[getRatingFromUserID] FindOne failed for userID=%d: %v", uID, err)
		return nil, err
	}

	score = &ScoreResult{
		Rating: int(user.Counter + user.VoteCounter*VOTE_RATING_MULTIPLY),
		Userid: user.Uid,
	}
	return score, nil
}

func getRatingFromUsername(ctx context.Context, username string) (score *ScoreResult, err error) {
	filter := bson.D{
		{Key: "username", Value: strings.ToLower(username)},
	}
	result := usersCollection.FindOne(ctx, filter)
	var user UserRecord
	err = result.Decode(&user)
	if err != nil {
		zap.S().Infof("[getRatingFromUsername] FindOne failed for username=%q: %v", username, err)
		return nil, err
	}

	score = &ScoreResult{
		Rating: int(user.Counter + user.VoteCounter*VOTE_RATING_MULTIPLY),
		Userid: user.Uid,
	}
	return score, nil
}

// getUser returns the UserRecord for the given Telegram user ID from MongoDB.
// Returns an error if the user is not in the database.
func getUser(ctx context.Context, uID int64) (*UserRecord, error) {
	filter := bson.D{{Key: "uid", Value: uID}}
	result := usersCollection.FindOne(ctx, filter)
	var user UserRecord
	if err := result.Decode(&user); err != nil {
		zap.S().Infof("[getUser] FindOne failed for userID=%d: %v", uID, err)
		return nil, err
	}
	return &user, nil
}

func pushBanLog(ctx context.Context, banInfo *BanInfo) {
	_, err := banLogs.InsertOne(ctx, banInfo)
	if err != nil {
		zap.S().Infof("[pushBanLog] insert failed for userID=%d chatID=%d type=%d: %v", banInfo.UserID, banInfo.ChatID, banInfo.Type, err)
	}

}

func saveMessage(ctx context.Context, message *ChatMessage) {
	_, err := chatMessages.InsertOne(ctx, message)
	if err != nil {
		zap.S().Infof("[saveMessage] insert failed for messageID=%d chatID=%d userID=%d: %v", message.MessageID, message.ChatID, message.UserID, err)
		return
	}
}

func updateMessage(ctx context.Context, message *ChatMessage) {
	filter := bson.D{
		{Key: "messageid", Value: message.MessageID},
		{Key: "chatid", Value: message.ChatID},
	}
	updatePipeline := bson.A{
		bson.D{
			{Key: "$set", Value: bson.D{
				{Key: "text", Value: bson.D{
					{Key: "$cond", Value: bson.D{
						{Key: "if", Value: bson.D{
							{Key: "$eq", Value: bson.A{"$text", message.Text}},
						}},
						{Key: "then", Value: "$text"},
						{Key: "else", Value: bson.D{
							{Key: "$concat", Value: bson.A{"$text", "\nEdit:\n", message.Text}},
						}},
					}},
				}},
			}},
		},
	}
	_, err := chatMessages.UpdateMany(ctx, filter, updatePipeline)
	if err != nil {
		zap.S().Infof("[updateMessage] UpdateMany failed for messageID=%d chatID=%d: %v", message.MessageID, message.ChatID, err)
		return
	}
}

func getMessageInfo(ctx context.Context, chatID int64, messageID int64) (chatMessage *ChatMessage, err error) {
	filter := bson.D{
		{Key: "chatid", Value: chatID},
		{Key: "messageid", Value: messageID},
	}
	result := chatMessages.FindOne(ctx, filter)
	var message ChatMessage
	err = result.Decode(&message)
	if err != nil {
		zap.S().Infof("[getMessageInfo] FindOne failed for messageID=%d chatID=%d: %v", messageID, chatID, err)
		return nil, err
	}
	return &message, nil
}

func readChatsSettings(ctx context.Context) (ret map[int64]*DynamicSetting) {
	filter := bson.D{}
	cursor, err := chatSettingsCollection.Find(ctx, filter)
	if err != nil {
		zap.S().Panicf("[readChatsSettings] Find failed: %v", err)
	}
	ret = make(map[int64]*DynamicSetting)
	for cursor.Next(ctx) {
		var result DynamicSetting
		err := cursor.Decode(&result)
		if err != nil {
			zap.S().Infof("[readChatsSettings] Decode failed: %v", err)
			continue
		}
		ret[result.ChatID] = &result
	}
	return ret

}

func writeChatSettings(ctx context.Context, chatID int64, settings *DynamicSetting) {
	filter := bson.D{
		{Key: "chatid", Value: chatID},
	}
	update := bson.D{
		{Key: "$set", Value: settings},
	}

	_, err := chatSettingsCollection.UpdateOne(ctx, filter, update, upsertOptions)
	if err != nil {
		zap.S().Infof("[writeChatSettings] upsert failed for chatID=%d: %v", chatID, err)
	}

}

func deleteChatSettings(ctx context.Context, chatID int64) {
	filter := bson.D{
		{Key: "chatid", Value: chatID},
	}
	_, err := chatSettingsCollection.DeleteOne(ctx, filter)
	if err != nil {
		zap.S().Infof("[deleteChatSettings] DeleteOne failed for chatID=%d: %v", chatID, err)
	}
}

func getUserLastNthMessages(ctx context.Context, userID int64, chatID int64, amount uint16) (ret []ChatMessage, err error) {
	filter := bson.D{
		{Key: "chatid", Value: chatID},
		{Key: "userid", Value: userID},
	}
	options := options.Find().SetSort(bson.D{{Key: "date", Value: -1}}).SetLimit(int64(amount))
	cursor, err := chatMessages.Find(ctx, filter, options)
	if err != nil {
		zap.S().Infof("[getUserLastNthMessages] Find failed for userID=%d chatID=%d limit=%d: %v", userID, chatID, amount, err)
		return nil, err
	}
	err = cursor.All(ctx, &ret)
	if err != nil {
		zap.S().Infof("[getUserLastNthMessages] cursor.All failed for userID=%d chatID=%d: %v", userID, chatID, err)
		return nil, err
	}
	return ret, nil
}

// getUserLastDaysMessages returns all messages for the user in the given chat
// that were stored within the last [days] days.
func getUserLastDaysMessages(ctx context.Context, userID int64, chatID int64, days int) (ret []ChatMessage, err error) {
	since := uint64(time.Now().AddDate(0, 0, -days).Unix())
	filter := bson.D{
		{Key: "chatid", Value: chatID},
		{Key: "userid", Value: userID},
		{Key: "date", Value: bson.D{{Key: "$gte", Value: since}}},
	}
	cursor, err := chatMessages.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "date", Value: -1}}))
	if err != nil {
		zap.S().Infof("[getUserLastDaysMessages] Find failed for userID=%d chatID=%d days=%d: %v", userID, chatID, days, err)
		return nil, err
	}
	err = cursor.All(ctx, &ret)
	if err != nil {
		zap.S().Infof("[getUserLastDaysMessages] cursor.All failed for userID=%d chatID=%d: %v", userID, chatID, err)
		return nil, err
	}
	return ret, nil
}
