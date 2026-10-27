package bot

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"sync"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/ice2heart/poke_bot/internal/storage"
	"go.uber.org/zap"
)

// Pictures the bot attaches to its own messages. They must be embedded: the
// production image is FROM scratch and ships only the binary, so reading these
// files at runtime would always fail.

//go:embed cccp.jpg
var cccpJPG []byte

// newImages builds the registry of every embedded picture, keyed by the short
// name used to look one up and to key its cached file_id. Adding a picture is
// one //go:embed above and one entry here — nothing else in the codebase
// changes. Each App gets its own registry, since the cached file_id is mutable.
func newImages() map[string]*embeddedImage {
	return map[string]*embeddedImage{
		"cccp": {filename: "cccp.jpg", data: cccpJPG},
	}
}

// errUnknownImage is returned when a caller names a picture that is not in the
// registry — a programming error, so callers fall back to a plain message.
var errUnknownImage = errors.New("unknown embedded image")

// embeddedImage is a picture compiled into the binary, plus the Telegram
// file_id it was last uploaded under. Uploading is expensive and a file_id is
// valid for the bot in every chat, so the id is cached in memory and persisted
// (see DynamicSetting.FileIDs) to survive restarts.
type embeddedImage struct {
	filename string
	data     []byte

	// mu guards fileID only; it is never held across a network call.
	mu     sync.Mutex
	fileID string
}

// cachedID returns the process-cached file_id, or "" if this image has not been
// uploaded in this process yet.
func (im *embeddedImage) cachedID() string {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.fileID
}

// setCachedID updates the process cache.
func (im *embeddedImage) setCachedID(id string) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.fileID = id
}

// clearCachedID drops id from the process cache if it is still the current one.
func (im *embeddedImage) clearCachedID(id string) {
	im.mu.Lock()
	defer im.mu.Unlock()
	if im.fileID == id {
		im.fileID = ""
	}
}

// payload returns the cached file_id reference, or a fresh multipart upload.
// A new bytes.Reader per call, so concurrent sends never share a drained one.
func (im *embeddedImage) payload(fileID string) models.InputFile {
	if fileID != "" {
		return &models.InputFileString{Data: fileID}
	}
	return &models.InputFileUpload{Filename: im.filename, Data: bytes.NewReader(im.data)}
}

// loadImageFileID returns the known file_id for name: the process cache if
// warm, otherwise the first one persisted on any chat's settings. A file_id is
// valid for the bot in every chat, so whichever chat recorded it serves all.
func (a *App) loadImageFileID(name string, im *embeddedImage) string {
	if id := im.cachedID(); id != "" {
		return id
	}

	a.settingsMux.Lock()
	found := ""
	for _, s := range a.settings {
		if id := s.FileIDs[name]; id != "" {
			found = id
			break
		}
	}
	a.settingsMux.Unlock()

	if found != "" {
		im.setCachedID(found)
	}
	return found
}

// storeImageFileID records a freshly uploaded file_id in the process cache and
// persists it on this chat's settings so it survives a restart.
func (a *App) storeImageFileID(ctx context.Context, name string, im *embeddedImage, chatID int64, fileID string) {
	im.setCachedID(fileID)

	a.settingsMux.Lock()
	defer a.settingsMux.Unlock()
	chatSettings := a.getChatSettings(ctx, chatID)
	if chatSettings.FileIDs == nil {
		chatSettings.FileIDs = map[string]string{}
	}
	chatSettings.FileIDs[name] = fileID
	storage.WriteChatSettings(ctx, chatID, chatSettings)
}

// clearImageFileID drops an id Telegram rejected, from the process cache and
// from every chat that had recorded it.
func (a *App) clearImageFileID(ctx context.Context, name string, im *embeddedImage, stale string) {
	im.clearCachedID(stale)

	a.settingsMux.Lock()
	defer a.settingsMux.Unlock()
	for chatID, s := range a.settings {
		if s.FileIDs[name] == stale {
			delete(s.FileIDs, name)
			storage.WriteChatSettings(ctx, chatID, s)
		}
	}
}

// sendImage sends params with the named embedded picture attached, reusing the
// stored file_id when known and recording it after the first upload. Validity
// is checked lazily: Telegram offers no cheap probe for a file_id (getFile
// costs a round-trip on every send and can succeed while the send still
// fails), so a rejected id is dropped and retried once as a real upload. That
// self-heals a stale id without paying for a check on the sends that are fine.
//
// Neither mutex is held across the network call: settingsMux is process-wide,
// and holding it through an upload would stall every other chat.
func (a *App) sendImage(ctx context.Context, b *bot.Bot, name string, chatID int64, params *bot.SendPhotoParams) (*models.Message, error) {
	im, ok := a.images[name]
	if !ok {
		zap.S().Infof("[sendImage] unknown image %q, sending without it", name)
		return nil, errUnknownImage
	}

	cached := a.loadImageFileID(name, im)

	params.Photo = im.payload(cached)
	msg, err := b.SendPhoto(ctx, params)
	if err != nil && cached != "" {
		zap.S().Infof("[sendImage] stored file_id for %q rejected, re-uploading: %v", name, err)
		a.clearImageFileID(ctx, name, im, cached)
		cached = ""
		params.Photo = im.payload("")
		msg, err = b.SendPhoto(ctx, params)
	}
	if err != nil {
		return nil, err
	}
	if cached == "" && msg != nil && len(msg.Photo) > 0 {
		newID := msg.Photo[len(msg.Photo)-1].FileID // largest size last
		a.storeImageFileID(ctx, name, im, chatID, newID)
		zap.S().Infof("[sendImage] stored %q file_id=%s chatID=%d", name, newID, chatID)
	}
	return msg, nil
}
