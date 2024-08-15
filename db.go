package main

import (
	"context"
	"log"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var (
	// client   *mongo.Client
	dataBase *mongo.Database

	usersCollection        *mongo.Collection
	banLogs                *mongo.Collection
	chatMessages           *mongo.Collection
	chatSettingsCollection *mongo.Collection

	upserOptions *options.UpdateOptions
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

type ScoreResult struct {
	Rating int   `bson:"rating"`
	Userid int64 `bson:"userid"`
}

type DyncmicSetting struct {
	ChatID        int64
	Pause         bool
	LogRecipients []int64
	ChatName      string
	ChatUsername  string
}

func initDb(ctx context.Context, connectionLine string, dbName string) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(connectionLine))
	if err != nil {
		panic(err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		panic(err)
	}
	log.Printf("MongoDB is connected")
	upserOptions = &options.UpdateOptions{}
	upserOptions.SetUpsert(true)
	dataBase = client.Database(dbName)
	usersCollection = dataBase.Collection("users")
	banLogs = dataBase.Collection("ban_log")
	chatMessages = dataBase.Collection("messages")
	chatSettingsCollection = dataBase.Collection("settings")
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
	_, err := usersCollection.UpdateOne(ctx, filter, update, upserOptions)
	if err != nil {
		log.Printf("Upsert of user counter went wrong %v", err)
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
	_, err := usersCollection.UpdateOne(ctx, filter, update, upserOptions)
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
	_, err := usersCollection.UpdateOne(ctx, filter, update, upserOptions)
	if err != nil {
		log.Printf("Upsert of user vote maker went wrong %v", err)
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
		log.Printf("Can't get user score %v", err)
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
		log.Printf("Can't get user score %v", err)
		return nil, err
	}

	score = &ScoreResult{
		Rating: int(user.Counter + user.VoteCounter*VOTE_RATING_MULTIPLY),
		Userid: user.Uid,
	}
	return score, nil
}

func getUser(ctx context.Context, uID int64) (userRecord *UserRecord, err error) {
	filter := bson.D{
		{Key: "uid", Value: uID},
	}
	result := usersCollection.FindOne(ctx, filter)
	var user UserRecord
	err = result.Decode(&user)
	if err != nil {
		log.Printf("Can't get user score %v", err)
		return nil, err
	}
	return &user, nil
}

func pushBanLog(ctx context.Context, banInfo *BanInfo) {
	_, err := banLogs.InsertOne(ctx, banInfo)
	if err != nil {
		log.Printf("Can't insert ban info %v", err)
	}

}

func saveMessage(ctx context.Context, message *ChatMessage) {
	_, err := chatMessages.InsertOne(ctx, message)
	if err != nil {
		log.Printf("Can't insert message %v", err)
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
					{Key: "$concat", Value: bson.A{
						"$text", "\nEdit:\n", message.Text,
					}},
				}},
			},
			},
		},
	}
	_, err := chatMessages.UpdateMany(ctx, filter, updatePipeline)
	if err != nil {
		log.Printf("Can't update edited message %v", err)
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
		log.Printf("Cant't get message info: %v", err)
		return nil, err
	}
	return &message, nil
}

func readChatsSettings(ctx context.Context) (ret map[int64]*DyncmicSetting) {
	filter := bson.D{}
	cursor, err := chatSettingsCollection.Find(ctx, filter)
	if err != nil {
		log.Panic("Can't read settings")
	}
	ret = make(map[int64]*DyncmicSetting)
	for cursor.Next(ctx) {
		var result DyncmicSetting
		err := cursor.Decode(&result)
		if err != nil {
			log.Println(err)
			continue
		}
		ret[result.ChatID] = &result
	}
	return ret

}

func writeChatSettings(ctx context.Context, chatID int64, settings *DyncmicSetting) {
	filter := bson.D{
		{Key: "chatid", Value: chatID},
	}
	update := bson.D{
		{Key: "$set", Value: settings},
	}

	_, err := chatSettingsCollection.UpdateOne(ctx, filter, update, upserOptions)
	if err != nil {
		log.Printf("Upsert of the chat settings went wrong %v", err)
	}

}

func deleteChatSettings(ctx context.Context, chatID int64) {
	filter := bson.D{
		{Key: "chatid", Value: chatID},
	}
	_, err := chatSettingsCollection.DeleteOne(ctx, filter)
	if err != nil {
		log.Printf("Delete of the chat settings went wrong %v", err)
	}
}

func getUserLastNthMessages(ctx context.Context, userID int64, chatID int64, amaount uint16) (ret []ChatMessage, err error) {
	filter := bson.D{
		{Key: "chatid", Value: chatID},
		{Key: "userid", Value: userID},
	}
	options := options.Find().SetSort(bson.D{{Key: "$natural", Value: -1}}).SetLimit(int64(amaount))
	cursor, err := chatMessages.Find(ctx, filter, options)
	if err != nil {
		log.Printf("Cant't get last %dth elemets: %v", amaount, err)
		return nil, err
	}
	err = cursor.All(ctx, &ret)
	if err != nil {
		log.Printf("Cant't parse last %dth elemets: %v", amaount, err)
		return nil, err
	}
	return ret, nil
}
