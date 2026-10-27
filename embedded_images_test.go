package main

import (
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
)

func TestEmbeddedImages(t *testing.T) {
	// Every registered image must actually be embedded and be a real JPEG,
	// so a new entry with a typo'd filename fails here rather than in Telegram.
	for name, im := range images {
		t.Run(name, func(t *testing.T) {
			assert.NotEmpty(t, im.filename, "filename must be set")
			assert.NotEmpty(t, im.data, "%s must be embedded in the binary", im.filename)
			assert.Equal(t, []byte{0xFF, 0xD8, 0xFF}, im.data[:3], "expected JPEG magic bytes")
		})
	}

	assert.Contains(t, images, "cccp", "the cccp image must stay registered")
}

func TestLoadImageFileIDReadsPersistedSetting(t *testing.T) {
	// settings and the process cache are package globals; restore both so this
	// test cannot leak into others.
	im := &embeddedImage{filename: "test.jpg", data: []byte{0xFF, 0xD8, 0xFF}}
	origSettings := settings
	t.Cleanup(func() { settings = origSettings })

	// A chat with no stored id must not produce one.
	settings = map[int64]*DynamicSetting{
		-100: {ChatID: -100},
	}
	assert.Equal(t, "", loadImageFileID("test", im))

	// Once any chat has recorded an id, it serves every chat.
	settings = map[int64]*DynamicSetting{
		-100: {ChatID: -100},
		-200: {ChatID: -200, FileIDs: map[string]string{"test": "STORED_FILE_ID"}},
	}
	assert.Equal(t, "STORED_FILE_ID", loadImageFileID("test", im))

	// The value is now in the process cache: it survives the settings map
	// losing it, which is what makes the common path Mongo-free.
	settings = map[int64]*DynamicSetting{}
	assert.Equal(t, "STORED_FILE_ID", loadImageFileID("test", im))
}

func TestLoadImageFileIDIsKeyedByName(t *testing.T) {
	// One chat's FileIDs map holds every image; a lookup must not pick up
	// another image's id.
	origSettings := settings
	t.Cleanup(func() { settings = origSettings })

	settings = map[int64]*DynamicSetting{
		-100: {ChatID: -100, FileIDs: map[string]string{"other": "OTHER_ID"}},
	}
	assert.Equal(t, "", loadImageFileID("test", &embeddedImage{filename: "test.jpg"}))
	assert.Equal(t, "OTHER_ID", loadImageFileID("other", &embeddedImage{filename: "other.jpg"}))
}

func TestImagePayloadSelection(t *testing.T) {
	im := &embeddedImage{filename: "test.jpg", data: []byte{0xFF, 0xD8, 0xFF}}

	upload, ok := im.payload("").(*models.InputFileUpload)
	assert.True(t, ok, "an empty file_id must produce a multipart upload")
	assert.Equal(t, "test.jpg", upload.Filename)

	ref, ok := im.payload("CACHED_ID").(*models.InputFileString)
	assert.True(t, ok, "a known file_id must produce a file_id reference")
	assert.Equal(t, "CACHED_ID", ref.Data)
}

func TestSendImageRejectsUnknownName(t *testing.T) {
	_, err := sendImage(t.Context(), nil, "no_such_image", -100, &bot.SendPhotoParams{})
	assert.ErrorIs(t, err, errUnknownImage)
}
