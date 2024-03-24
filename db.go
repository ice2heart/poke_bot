package main

import (
	"context"
	"errors"
	"log"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var (
	// client   *mongo.Client
	dataBase *mongo.Database

	usersCollection *mongo.Collection
	banLogs         *mongo.Collection
	chatMessages    *mongo.Collection

	upserOptions *options.UpdateOptions
)

// DB schema
// uid - index int64
// counter - inc counter
// voteCounter - inc counter

type UserRecord struct {
	ID          primitive.ObjectID `bson:"_id"`
	Uid         int64
	Counter     uint32
	VoteCounter uint32
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
}

func userPlusOneMessage(ctx context.Context, uID int64) {
	filter := bson.D{
		{Key: "uid", Value: uID},
	}
	update := bson.D{
		{Key: "$inc", Value: bson.D{
			{Key: "counter", Value: 1},
		}},
	}
	result, err := usersCollection.UpdateOne(ctx, filter, update, upserOptions)
	if err != nil {
		log.Printf("Upsert of user counter went wrong %v", err)
	}
	log.Printf("Updated ids %v, modified %d, upserted %d", result.UpsertedID, result.ModifiedCount, result.UpsertedCount)
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
	result, err := usersCollection.UpdateOne(ctx, filter, update, upserOptions)
	if err != nil {
		log.Printf("Upsert of user vote maker went wrong %v", err)
	}
	log.Printf("Created ids %v, modified %d, upserted %d", result.UpsertedID, result.ModifiedCount, result.UpsertedCount)
}

func getUserScore(ctx context.Context, uID int64) (score int64, err error) {
	filter := bson.D{
		{Key: "uid", Value: uID},
	}
	result := usersCollection.FindOne(ctx, filter)
	var user UserRecord
	err = result.Decode(&user)
	if err != nil {
		log.Printf("Can't get user score %v", err)
		return 0, err
	}

	score = int64(user.Counter) + int64(user.VoteCounter)*100
	return score, nil
}

func pushBanLog(ctx context.Context, uID int64, userInfo string, from int64) {

}

func saveMessage(ctx context.Context, message *ChatMessage) {
	log.Printf("Pushing message %s", message.Text)
	result, err := chatMessages.InsertOne(ctx, message)
	if err != nil {
		log.Printf("Can't insert message %v", err)
		return
	}
	log.Printf("Inserted the message %v", result.InsertedID)
}

func getRatingFromMessage(ctx context.Context, chatID int64, messageID int64) (score *ScoreResult, err error) {
	getMessage := bson.D{
		{Key: "$match",
			Value: bson.D{
				{Key: "chatid", Value: chatID},
				{Key: "messageid", Value: messageID},
			},
		},
	}
	lookupUser := bson.D{
		{Key: "$lookup",
			Value: bson.D{
				{Key: "from", Value: "users"},
				{Key: "localField", Value: "userid"},
				{Key: "foreignField", Value: "uid"},
				{Key: "as", Value: "result"},
			},
		},
	}
	unwindUser := bson.D{{Key: "$unwind", Value: bson.D{{Key: "path", Value: "$result"}}}}
	calculateRating := bson.D{
		{Key: "$set",
			Value: bson.D{
				{Key: "rating",
					Value: bson.D{
						{Key: "$add",
							Value: bson.A{
								bson.D{
									{Key: "$ifNull",
										Value: bson.A{
											"$result.counter",
											0,
										},
									},
								},
								bson.D{
									{Key: "$multiply",
										Value: bson.A{
											bson.D{
												{Key: "$ifNull",
													Value: bson.A{
														"$result.voteCounter",
														0,
													},
												},
											},
											10,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	clearOutput := bson.D{
		{Key: "$project",
			Value: bson.D{
				{Key: "rating", Value: 1},
				{Key: "userid", Value: 1},
			},
		},
	}
	cursor, err := chatMessages.Aggregate(ctx, mongo.Pipeline{getMessage, lookupUser, unwindUser, calculateRating, clearOutput})
	if err != nil {
		log.Printf("Can't get proper rating for message %v", err)
		return nil, err
	}

	var results []ScoreResult
	if err = cursor.All(ctx, &results); err != nil {
		log.Printf("Problem with parsing cursor")
		return nil, errors.New("mongo: can't find message")
	}
	if len(results) == 0 {
		log.Printf("Message not found")
		return nil, errors.New("mongo: can't find message")
	}
	log.Printf("Get rating %d for user %d", results[0].Rating, results[0].Userid)
	return &results[0], nil
}
