package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/ice2heart/poke_bot/internal/bot"
)

func main() {
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

	superAdminID, err := strconv.ParseInt(superAdminIDString, 10, 64)
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

	bot.Run(ctx, bot.Config{
		BotAPIKey:    botApiKey,
		MongoAddr:    mongoAddr,
		DBName:       dbName,
		SuperAdminID: superAdminID,
		AppID:        int(appId),
		AppHash:      appHash,
		Logger:       logger,
	})
}
