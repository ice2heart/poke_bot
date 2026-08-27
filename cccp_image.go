package main

import (
	"bytes"
	"context"
	_ "embed"
	"sync"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"
)

// cccpImage is the picture attached to the mute notification when the command
// was invoked as /mute^cccp. It must be embedded: the production image is
// FROM scratch and ships only the binary, so reading the file at runtime would
// always fail.
//
//go:embed cccp.jpg
var cccpImage []byte

const cccpImageFilename = "cccp.jpg"

// cccpFileIDMem is the process-local fast path over the persisted id, so the
// common case touches neither Mongo nor settingsMux.
var cccpFileIDMem struct {
	mu sync.Mutex
	id string
}

// cccpLoadFileID returns the known file_id: the process cache if warm,
// otherwise the first one persisted on any chat's settings. A file_id is valid
// for the bot in every chat, so whichever chat recorded it serves all of them.
func cccpLoadFileID(ctx context.Context) string {
	cccpFileIDMem.mu.Lock()
	if id := cccpFileIDMem.id; id != "" {
		cccpFileIDMem.mu.Unlock()
		return id
	}
	cccpFileIDMem.mu.Unlock()

	settingsMux.Lock()
	found := ""
	for _, s := range settings {
		if s.CccpFileID != "" {
			found = s.CccpFileID
			break
		}
	}
	settingsMux.Unlock()

	if found != "" {
		cccpFileIDMem.mu.Lock()
		cccpFileIDMem.id = found
		cccpFileIDMem.mu.Unlock()
	}
	return found
}

// cccpStoreFileID records a freshly uploaded file_id in the process cache and
// persists it on this chat's settings so it survives a restart.
func cccpStoreFileID(ctx context.Context, chatID int64, fileID string) {
	cccpFileIDMem.mu.Lock()
	cccpFileIDMem.id = fileID
	cccpFileIDMem.mu.Unlock()

	settingsMux.Lock()
	defer settingsMux.Unlock()
	chatSettings := getChatSettings(ctx, chatID)
	chatSettings.CccpFileID = fileID
	writeChatSettings(ctx, chatID, chatSettings)
}

// cccpClearFileID drops an id Telegram rejected, from the process cache and
// from every chat that had recorded it.
func cccpClearFileID(ctx context.Context, stale string) {
	cccpFileIDMem.mu.Lock()
	if cccpFileIDMem.id == stale {
		cccpFileIDMem.id = ""
	}
	cccpFileIDMem.mu.Unlock()

	settingsMux.Lock()
	defer settingsMux.Unlock()
	for chatID, s := range settings {
		if s.CccpFileID == stale {
			s.CccpFileID = ""
			writeChatSettings(ctx, chatID, s)
		}
	}
}

// cccpPhoto returns the cached file_id reference, or a fresh multipart upload.
// A new bytes.Reader per call, so concurrent sends never share a drained one.
func cccpPhoto(fileID string) models.InputFile {
	if fileID != "" {
		return &models.InputFileString{Data: fileID}
	}
	return &models.InputFileUpload{Filename: cccpImageFilename, Data: bytes.NewReader(cccpImage)}
}

// sendCccpPhoto sends params with cccp.jpg attached, reusing the stored file_id
// when known and recording it after the first upload. Validity is checked
// lazily: Telegram offers no cheap probe for a file_id (getFile costs a
// round-trip on every send and can succeed while the send still fails), so a
// rejected id is dropped and retried once as a real upload. That self-heals a
// stale id without paying for a check on the sends that are fine.
//
// Neither mutex is held across the network call: settingsMux is process-wide,
// and holding it through an upload would stall every other chat.
func sendCccpPhoto(ctx context.Context, b *bot.Bot, chatID int64, params *bot.SendPhotoParams) (*models.Message, error) {
	cached := cccpLoadFileID(ctx)

	params.Photo = cccpPhoto(cached)
	msg, err := b.SendPhoto(ctx, params)
	if err != nil && cached != "" {
		zap.S().Infof("[sendCccpPhoto] stored file_id rejected, re-uploading: %v", err)
		cccpClearFileID(ctx, cached)
		cached = ""
		params.Photo = cccpPhoto("")
		msg, err = b.SendPhoto(ctx, params)
	}
	if err != nil {
		return nil, err
	}
	if cached == "" && msg != nil && len(msg.Photo) > 0 {
		newID := msg.Photo[len(msg.Photo)-1].FileID // largest size last
		cccpStoreFileID(ctx, chatID, newID)
		zap.S().Infof("[sendCccpPhoto] stored cccp.jpg file_id=%s chatID=%d", newID, chatID)
	}
	return msg, nil
}
