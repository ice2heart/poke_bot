package main

import (
	"context"

	"go.uber.org/zap"
)

// resolveUser returns the UserRecord for uID, using MongoDB as the primary
// source and falling back to MTProto when the record is absent.
// If MTProto resolves the user, the record is upserted into MongoDB so
// subsequent calls can find it via DB alone.
// Returns an error only when both sources fail.
func resolveUser(ctx context.Context, uID int64) (*UserRecord, error) {
	user, err := getUser(ctx, uID)
	if err == nil {
		return user, nil
	}

	zap.S().Infof("[resolveUser] userID=%d not in DB, trying MTProto", uID)
	mtUser, mtErr := client.GetUser(ctx, uID)
	if mtErr != nil {
		zap.S().Infof("[resolveUser] MTProto failed for userID=%d: %v", uID, mtErr)
		return nil, err // return original DB error
	}

	username := mtUser.Username
	zap.S().Infof("[resolveUser] MTProto resolved userID=%d username=%q", uID, username)
	if dbErr := ensureUser(ctx, uID, username, username); dbErr != nil {
		zap.S().Infof("[resolveUser] ensureUser failed for userID=%d: %v", uID, dbErr)
	}

	return &UserRecord{Uid: uID, Username: username, AltUsername: username}, nil
}

// prepareBanInfo fills the user-related and last-message fields of a BanInfo
// for the given chatID/userID. It never returns an error: if the user cannot
// be resolved via DB or MTProto, placeholder values are used so the caller
// can always proceed with a valid struct.
// The caller is responsible for setting Type and BanMessage.
func prepareBanInfo(ctx context.Context, chatID, userID int64) *BanInfo {
	banInfo := &BanInfo{
		ChatID: chatID,
		UserID: userID,
	}

	user, err := resolveUser(ctx, userID)
	if err != nil {
		zap.S().Infof("[prepareBanInfo] could not resolve userID=%d, using placeholder", userID)
		banInfo.Score = LOW_SCORE
		banInfo.LastMessage = "Сообщение не найдено"
		return banInfo
	}
	banInfo.ProfileName = user.AltUsername
	banInfo.UserName = user.Username
	banInfo.Score = calculateRequiredRating(user.Counter)

	messages, err := getUserLastNthMessages(ctx, userID, chatID, 1)
	if err != nil || len(messages) == 0 {
		banInfo.LastMessage = "Сообщение не найдено"
	} else {
		banInfo.LastMessage = messages[0].Text
		banInfo.TargetMessageID = messages[0].MessageID
	}
	return banInfo
}
