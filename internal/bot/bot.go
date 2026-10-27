package bot

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"go.uber.org/zap"

	"github.com/ice2heart/poke_bot/internal/cache"
	"github.com/ice2heart/poke_bot/internal/mtproto"
	"github.com/ice2heart/poke_bot/internal/storage"
)

// Config carries everything Run needs, already read from the environment.
type Config struct {
	BotAPIKey    string
	MongoAddr    string
	DBName       string
	SuperAdminID int64
	AppID        int
	AppHash      string
	Logger       *zap.Logger
}

// go delay(ctx, 10, func() { log.Printf("Delayed call") })
func delay(ctx context.Context, delaySeconds int64, arg func()) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(delaySeconds * int64(time.Second))):
		arg()
	}
}

func ticker(ctx context.Context, delaySeconds int64, arg func(context.Context)) {
	ticker := time.NewTicker(time.Duration(delaySeconds * int64(time.Second)))
	for {
		select {
		case <-ctx.Done():
			// fmt.Println("Context kill")
			return
		case <-ticker.C:
			// case t := <-ticker.C:
			// fmt.Println("Tick at", t)
			arg(ctx)
		}
	}
}

// App holds all the state of one running bot, so that two Run calls share
// nothing.
type App struct {
	bot    *bot.Bot
	client *mtproto.MTProtoHelper

	myID         string
	superAdminID int64

	// Lock order: settingsMux -> adminsMux. sessionsMux is taken before the
	// ban cache's own (leaf) lock.
	settingsMux sync.Mutex
	settings    map[int64]*storage.DynamicSetting

	adminsMux sync.Mutex
	admins    map[int64]map[int64]bool

	sessionsMux sync.Mutex
	sessions    map[int64]map[int64]*BanInfo

	banCache *banInfoCache

	// detectorCh receives all updates for spam analysis.
	detectorCh    chan *models.Update
	reactionCache *cache.Cache[reactionKey, reactionEntry]
	patterns      *patternCache

	// images is this App's registry of embedded pictures and their cached file_ids.
	images map[string]*embeddedImage

	// Command handlers built from voteHandlerConfig, see newVoteHandlers.
	banHandler      bot.HandlerFunc
	muteHandler     bot.HandlerFunc
	textOnlyHandler bot.HandlerFunc
}

// newApp builds an App with empty state. ctx is the parent of the ban caches.
func newApp(ctx context.Context, superAdminID int64) *App {
	a := &App{
		superAdminID: superAdminID,
		sessions:     make(map[int64]map[int64]*BanInfo),
		banCache:     newBanInfoCache(ctx),
		detectorCh:   make(chan *models.Update, 128),
		patterns:     newPatternCache(),
		images:       newImages(),
	}
	a.newVoteHandlers()
	return a
}

// Run connects to MongoDB and Telegram, registers every handler and blocks
// until ctx is cancelled, then expires the remaining active votes.
func Run(ctx context.Context, cfg Config) {
	var err error
	a := newApp(ctx, cfg.SuperAdminID)

	storage.Init(ctx, cfg.MongoAddr, cfg.DBName)
	a.settings = storage.ReadChatsSettings(ctx)

	a.client = &mtproto.MTProtoHelper{AppId: cfg.AppID, AppHash: cfg.AppHash, BotApiKey: cfg.BotAPIKey, Logger: cfg.Logger}
	if err = a.client.Init(ctx); err != nil {
		zap.S().Panicf("[Run] MTProto init failed: %v", err)
	}
	defer a.client.Stop()

	for _, chatSettings := range a.settings {
		if chatSettings.ChatAccessHash != 0 {
			continue
		}
		accessHash, err := a.client.GetAccessHash(ctx, chatSettings.ChatID)
		if err != nil {
			zap.S().Infof("[Run] GetAccessHash failed for chatID=%d %q: %v", chatSettings.ChatID, chatSettings.ChatName, err)
		}
		chatSettings.ChatAccessHash = accessHash
		storage.WriteChatSettings(ctx, chatSettings.ChatID, chatSettings)
		zap.S().Infof("[Run] updated access hash for chatID=%d %q", chatSettings.ChatID, chatSettings.ChatName)
	}

	opts := []bot.Option{
		bot.WithDefaultHandler(handler),
		bot.WithMiddlewares(a.logMessagesMiddleware, a.detectorMiddleware),
		bot.WithCallbackQueryDataHandler("button", bot.MatchTypePrefix, a.voteCallbackHandler),
		bot.WithAllowedUpdates(bot.AllowedUpdates{"message", "edited_message", "callback_query", "my_chat_member", "message_reaction", "message_reaction_count"}),
	}

	a.bot, err = bot.New(cfg.BotAPIKey, opts...)
	if err != nil {
		panic(err)
	}
	me, err := a.bot.GetMe(ctx)
	if err != nil {
		panic(err)
	}
	a.myID = me.Username

	a.admins = make(map[int64]map[int64]bool)
	a.getChatAdmins(ctx)

	a.bot.RegisterHandler(bot.HandlerTypeMessageText, fmt.Sprintf("@%s", a.myID), bot.MatchTypePrefix, a.banHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/pause", bot.MatchTypePrefix, a.pauseHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/ban", bot.MatchTypePrefix, a.banHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/voteban", bot.MatchTypePrefix, a.banHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/voteblan", bot.MatchTypePrefix, a.banHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/mute", bot.MatchTypePrefix, a.muteHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/voteeblan", bot.MatchTypePrefix, a.muteHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/text_only", bot.MatchTypePrefix, a.textOnlyHandler)
	a.bot.RegisterHandler(bot.HandlerTypeCallbackQueryData, "b_", bot.MatchTypePrefix, a.actionCallbackHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/test", bot.MatchTypePrefix, testHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypePrefix, a.startHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/delete", bot.MatchTypePrefix, a.deleteMessageHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/set_channel", bot.MatchTypePrefix, a.setChannelHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/add_pattern", bot.MatchTypePrefix, a.addPatternHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/del_pattern", bot.MatchTypePrefix, a.delPatternHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/likes", bot.MatchTypePrefix, a.likesHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/best", bot.MatchTypePrefix, a.bestHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/set_tag", bot.MatchTypePrefix, setTagHandler)
	a.bot.RegisterHandler(bot.HandlerTypeMessageText, "/check", bot.MatchTypePrefix, a.checkHandler)
	zap.S().Infof("[Run] bot started as @%s userID=%d", me.Username, me.ID)
	// each 12 hours update admins list
	go ticker(ctx, 43200, a.getChatAdmins)
	// each 30 minutes expire votes older than 1.5 days
	go ticker(ctx, 1800, a.expireOldVotes)
	// spam edit detector
	a.reactionCache = cache.New[reactionKey, reactionEntry](ctx)
	go a.startDetector(ctx, a.bot)
	a.bot.Start(ctx)

	// Graceful shutdown: expire all remaining active votes.
	zap.S().Info("[Run] shutting down, expiring all active votes")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	a.expireAllVotes(shutdownCtx)
}

func handler(ctx context.Context, b *bot.Bot, update *models.Update) {

}
