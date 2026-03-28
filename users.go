package main

import (
	"context"
	"log"
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

	log.Printf("[resolveUser] userID=%d not in DB, trying MTProto", uID)
	mtUser, mtErr := client.GetUser(ctx, uID)
	if mtErr != nil {
		log.Printf("[resolveUser] MTProto failed for userID=%d: %v", uID, mtErr)
		return nil, err // return original DB error
	}

	username := mtUser.Username
	log.Printf("[resolveUser] MTProto resolved userID=%d username=%q", uID, username)
	if dbErr := ensureUser(ctx, uID, username, username); dbErr != nil {
		log.Printf("[resolveUser] ensureUser failed for userID=%d: %v", uID, dbErr)
	}

	return &UserRecord{Uid: uID, Username: username, AltUsername: username}, nil
}
