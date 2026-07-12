package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/ice2heart/poke_bot/cache"
	"github.com/ice2heart/poke_bot/mtproto"
)

var (
	myBot  *bot.Bot
	client *mtproto.MTProtoHelper

	myID         string
	superAdminID int64
)

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

func main() {
	var err error
	// TODO: replace default logger
	// https://github.com/uber-go/zap
	logger, _ := zap.NewDevelopment(zap.IncreaseLevel(zapcore.InfoLevel), zap.AddStacktrace(zapcore.FatalLevel))
	zap.ReplaceGlobals(logger)
	defer logger.Sync() // flushes buffer, if any
	godotenv.Load()
	botApiKey, ok := os.LookupEnv("BOT_API_KEY")
	if !ok {
		panic("BOT_API_KEY have to be set")
	}
	mongoAddr, ok := os.LookupEnv("MONGO_ADDRES")
	if !ok {
		mongoAddr = "mongodb://localhost:27017"
	}
	dbName, ok := os.LookupEnv("MONGO_DB_NAME")
	if !ok {
		dbName = "pokebot"
	}

	superAdminIDString, ok := os.LookupEnv("ADMIN_ID")
	if !ok {
		panic("ADMIN_ID have to be set")
	}

	superAdminID, err = strconv.ParseInt(superAdminIDString, 10, 64)
	if err != nil {
		zap.S().Panicf("[main] ADMIN_ID parse error: %v", err)
	}

	// // Grab those from https://my.telegram.org/apps.
	// appID := flag.Int("api-id", 0, "app id")
	// appHash := flag.String("api-hash", "hash", "app hash")
	// // Get it from bot father.

	appIdString, ok := os.LookupEnv("BOT_APP_ID")
	if !ok {
		zap.S().Panic("[main] BOT_APP_ID env var is required")
	}
	appId, err := strconv.ParseInt(appIdString, 10, 32)
	if err != nil {
		zap.S().Panicf("[main] BOT_APP_ID parse error: %v", err)
	}

	appHash, ok := os.LookupEnv("BOT_APP_HASH")
	if !ok {
		zap.S().Panic("[main] BOT_APP_HASH env var is required")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	zap.S().Info("beforemonga")

	initDb(ctx, mongoAddr, dbName)
	settings = readChatsSettings(ctx)

	client = &mtproto.MTProtoHelper{AppId: int(appId), AppHash: appHash, BotApiKey: botApiKey, Logger: logger}
	if err = client.Init(ctx); err != nil {
		zap.S().Panicf("[main] MTProto init failed: %v", err)
	}
	defer client.Stop()

	for _, chatSettings := range settings {
		if chatSettings.ChatAccessHash != 0 {
			continue
		}
		accessHash, err := client.GetAccessHash(ctx, chatSettings.ChatID)
		if err != nil {
			zap.S().Infof("[main] GetAccessHash failed for chatID=%d %q: %v", chatSettings.ChatID, chatSettings.ChatName, err)
		}
		chatSettings.ChatAccessHash = accessHash
		writeChatSettings(ctx, chatSettings.ChatID, chatSettings)
		zap.S().Infof("[main] updated access hash for chatID=%d %q", chatSettings.ChatID, chatSettings.ChatName)
	}

	opts := []bot.Option{
		bot.WithDefaultHandler(handler),
		bot.WithMiddlewares(logMessagesMiddleware, detectorMiddleware),
		bot.WithCallbackQueryDataHandler("button", bot.MatchTypePrefix, voteCallbackHandler),
		bot.WithAllowedUpdates(bot.AllowedUpdates{"message", "edited_message", "callback_query", "my_chat_member", "message_reaction", "message_reaction_count"}),
	}

	myBot, err = bot.New(botApiKey, opts...)
	if err != nil {
		panic(err)
	}
	me, err := myBot.GetMe(ctx)
	if err != nil {
		panic(err)
	}
	myID = me.Username

	admins = make(map[int64]map[int64]bool)
	getChatAdmins(ctx)

	myBot.RegisterHandler(bot.HandlerTypeMessageText, fmt.Sprintf("@%s", myID), bot.MatchTypePrefix, banHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/pause", bot.MatchTypePrefix, pauseHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/ban", bot.MatchTypePrefix, banHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/voteban", bot.MatchTypePrefix, banHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/voteblan", bot.MatchTypePrefix, banHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/mute", bot.MatchTypePrefix, muteHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/voteeblan", bot.MatchTypePrefix, muteHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/text_only", bot.MatchTypePrefix, textOnlyHandler)
	myBot.RegisterHandler(bot.HandlerTypeCallbackQueryData, "b_", bot.MatchTypePrefix, actionCallbackHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/test", bot.MatchTypePrefix, testHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypePrefix, startHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/delete", bot.MatchTypePrefix, deleteMessageHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/set_channel", bot.MatchTypePrefix, setChannelHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/likes", bot.MatchTypePrefix, likesHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/best", bot.MatchTypePrefix, bestHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/set_tag", bot.MatchTypePrefix, setTagHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/check", bot.MatchTypePrefix, checkHandler)
	zap.S().Infof("[main] bot started as @%s userID=%d", me.Username, me.ID)
	// each 12 hours update admins list
	go ticker(ctx, 43200, getChatAdmins)
	// each 30 minutes expire votes older than 1.5 days
	go ticker(ctx, 1800, expireOldVotes)
	// spam edit detector
	reactionCache = cache.New[reactionKey, reactionEntry](ctx)
	go startDetector(ctx, myBot)
	myBot.Start(ctx)

	// Graceful shutdown: expire all remaining active votes.
	zap.S().Info("[main] shutting down, expiring all active votes")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	expireAllVotes(shutdownCtx)
}

func handler(ctx context.Context, b *bot.Bot, update *models.Update) {

}
