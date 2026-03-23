package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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

const (
	MESSAGE_TTL_DAYS = 14
)

var (
	myBot  *bot.Bot
	client *mtproto.MTProtoHelper

	myID      string
	linkRegex *regexp.Regexp = regexp.MustCompile(`(?:\s*https://t\.me/(c/)?([\d\w]+)/(\d+))`)

sessionsMux sync.Mutex
	sessions    map[int64]map[int64]*BanInfo = make(map[int64]map[int64]*BanInfo)

	settingsMux sync.Mutex
	settings    map[int64]*DynamicSetting

	adminsMux sync.Mutex
	admins    map[int64]map[int64]bool
	banCache  map[int64]*cacheEntity[int64, struct{}] = make(map[int64]*cacheEntity[int64, struct{}])

	superAdminID int64

	ANSWER_OWN             string = "Нельзя голосовать в собственном голосовании"
	ANSWER_NOTBAN          string = "Голос против бана принят"
	ANSWER_BAN             string = "Голос за бан принят"
	ANSWER_MUTE            string = "Голос за мут принят"
	ANSWER_NOTMUTE         string = "Голос против мута принят"
	ANSWER_SOMETHING_WRONG string = "Произошла ошибка. Попробуйте позже"
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
		log.Panicf("[main] ADMIN_ID parse error: %v", err)
	}

	// // Grab those from https://my.telegram.org/apps.
	// appID := flag.Int("api-id", 0, "app id")
	// appHash := flag.String("api-hash", "hash", "app hash")
	// // Get it from bot father.

	appIdString, ok := os.LookupEnv("BOT_APP_ID")
	if !ok {
		log.Panic("[main] BOT_APP_ID env var is required")
	}
	appId, err := strconv.ParseInt(appIdString, 10, 32)
	if err != nil {
		log.Panicf("[main] BOT_APP_ID parse error: %v", err)
	}

	appHash, ok := os.LookupEnv("BOT_APP_HASH")
	if !ok {
		log.Panic("[main] BOT_APP_HASH env var is required")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	log.Println("beforemonga")

	initDb(ctx, mongoAddr, dbName)
	settings = readChatsSettings(ctx)

	client = &mtproto.MTProtoHelper{AppId: int(appId), AppHash: appHash, BotApiKey: botApiKey, Logger: logger}
	if err = client.Init(ctx); err != nil {
		log.Panicf("[main] MTProto init failed: %v", err)
	}
	defer client.Stop()

	for _, chatSettings := range settings {
		if chatSettings.ChatAccessHash != 0 {
			continue
		}
		accessHash, err := client.GetAccessHash(ctx, chatSettings.ChatID)
		if err != nil {
			log.Printf("[main] GetAccessHash failed for chatID=%d %q: %v", chatSettings.ChatID, chatSettings.ChatName, err)
		}
		chatSettings.ChatAccessHash = accessHash
		writeChatSettings(ctx, chatSettings.ChatID, chatSettings)
		log.Printf("[main] updated access hash for chatID=%d %q", chatSettings.ChatID, chatSettings.ChatName)
	}

	opts := []bot.Option{
		bot.WithDefaultHandler(handler),
		bot.WithMiddlewares(logMessagesMiddleware),
		bot.WithCallbackQueryDataHandler("button", bot.MatchTypePrefix, voteCallbackHandler),
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
	log.Printf("[main] bot started as @%s userID=%d", me.Username, me.ID)
	// each 12 hours update admins list
	go ticker(ctx, 43200, getChatAdmins)
	myBot.Start(ctx)
}

func botRemovedFromChat(ctx context.Context, chatID int64) {
	chatSettings, ok := settings[chatID]
	if !ok {
		chatSettings = &DynamicSetting{ChatName: "unknown name", ChatUsername: "unknown username"}
	}

	name := chatSettings.ChatName
	username := chatSettings.ChatUsername
	if ok {
		delete(settings, chatID)
	}

	deleteChatSettings(ctx, chatID)
	// adminsMux.Lock()
	_, ok = admins[chatID]
	if ok {
		delete(admins, chatID)
	}
	// adminsMux.Unlock()

	// sessionsMux.Lock()
	delete(banCache, chatID)
	// sessionsMux.Unlock()

	myBot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: 198082233,
		Text:   fmt.Sprintf("Bot removed from chat \"%s\" (@%s, id: %d)", name, username, chatID),
	})
}

func getChatAdmins(ctx context.Context) {
	settingsMux.Lock()
	defer settingsMux.Unlock()
	for k := range settings {
		chat, err := myBot.GetChat(ctx, &bot.GetChatParams{
			ChatID: k,
		})
		if err != nil {
			botRemovedFromChat(ctx, k)
			continue
		}
		updated := false
		if len(settings[k].ChatName) == 0 {
			settings[k].ChatName = chat.Title
			updated = true
		}
		if len(settings[k].ChatUsername) == 0 {
			settings[k].ChatUsername = chat.Username
			updated = true
		}
		if updated {
			writeChatSettings(ctx, k, settings[k])
		}

		adminsList, err := getAdmins(ctx, myBot, k)
		if err != nil {
			botRemovedFromChat(ctx, k)
			continue
		}
		adminsMux.Lock()
		admins[k] = adminsList
		adminsMux.Unlock()
	}
}

func logMessagesMiddleware(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		// jcart, _ := json.MarshalIndent(update, "", "\t")
		// fmt.Println(string(jcart))
		if update.MyChatMember != nil {

			if update.MyChatMember.NewChatMember.Banned != nil || update.MyChatMember.NewChatMember.Left != nil {
				botRemovedFromChat(ctx, update.MyChatMember.Chat.ID)
			}
			if update.MyChatMember.NewChatMember.Member != nil && update.MyChatMember.Chat.ID < 0 {
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: 198082233,
					Text:   fmt.Sprintf("Bot added to chat \"%s\" (@%s)", update.MyChatMember.Chat.Title, update.MyChatMember.Chat.Username),
				})
				settingsMux.Lock()
				getChatSettings(ctx, update.MyChatMember.Chat.ID)
				settingsMux.Unlock()
			}
		}

		if update.Message != nil {
			now := time.Now()

			userID := update.Message.From.ID
			userName := update.Message.From.Username
			altUserName := fmt.Sprintf("%s %s", update.Message.From.FirstName, update.Message.From.LastName)
			if update.Message.SenderChat != nil {
				userID = update.Message.SenderChat.ID
				userName = update.Message.SenderChat.Username
				altUserName = update.Message.SenderChat.Title
			}

			storedText := update.Message.Text
			if update.Message.Sticker != nil {
				storedText = fmt.Sprintf("Sticker: %s, pack: %s", update.Message.Sticker.Emoji, update.Message.Sticker.SetName)
			}
			if update.Message.Animation != nil {
				storedText = fmt.Sprintf("GIF: name %s", update.Message.Animation.FileName)
			}
			if len(update.Message.Caption) != 0 {
				storedText = fmt.Sprintf("Photo, text:\n%s", update.Message.Caption)
			}

			hiddenUrls := make([]string, 0)
			for _, v := range update.Message.CaptionEntities {
				if len(v.URL) != 0 {
					hiddenUrls = append(hiddenUrls, v.URL)
				}
			}
			for _, v := range update.Message.Entities {
				if len(v.URL) != 0 {
					hiddenUrls = append(hiddenUrls, v.URL)
				}
			}
			if len(hiddenUrls) != 0 {
				storedText = fmt.Sprintf("%s\n%s", storedText, strings.Join(hiddenUrls, "\n"))
			}
			// log.Println(storedText)
			if len(storedText) == 0 {
				// jcart, _ := json.MarshalIndent(update, "", "\t")
				// fmt.Println(string(jcart))
				if len(update.Message.Photo) != 0 {
					storedText = "A photo without text"
				}
				if update.Message.Video != nil {
					storedText = "A video without text"
				}
			}

			go userPlusOneMessage(ctx, userID, userName, altUserName)
			go saveMessage(ctx, &ChatMessage{
				MessageID: int64(update.Message.ID),
				ChatID:    update.Message.Chat.ID,
				UserID:    userID,
				UserName:  userName,
				Text:      storedText,
				Date:      uint64(now.AddDate(0, 0, MESSAGE_TTL_DAYS).UnixMilli()),
			})
		}
		if update.EditedMessage != nil {

			storedText := update.EditedMessage.Text
			if len(update.EditedMessage.Caption) != 0 {
				storedText = fmt.Sprintf("Photo, text:\n%s", update.EditedMessage.Caption)
			}
			hiddenUrls := make([]string, 0)
			for _, v := range update.EditedMessage.CaptionEntities {
				if len(v.URL) != 0 {
					hiddenUrls = append(hiddenUrls, v.URL)
				}
			}
			for _, v := range update.EditedMessage.Entities {
				if len(v.URL) != 0 {
					hiddenUrls = append(hiddenUrls, v.URL)
				}
			}
			if len(hiddenUrls) != 0 {
				storedText = fmt.Sprintf("%s\n%s", storedText, strings.Join(hiddenUrls, "\n"))
			}
			updateMessage(ctx, &ChatMessage{
				MessageID: int64(update.EditedMessage.ID),
				ChatID:    update.EditedMessage.Chat.ID,
				Text:      storedText,
			})

		}

		next(ctx, b, update)
	}
}

func getChatSettings(ctx context.Context, chatId int64) (chatSettings *DynamicSetting) {
	chatSettings, prs := settings[chatId]
	if !prs {
		name := ""
		username := ""
		chat, err := myBot.GetChat(ctx, &bot.GetChatParams{
			ChatID: chatId,
		})
		if err == nil {
			name = chat.Title
			username = chat.Username
		}
		chatSettings = &DynamicSetting{
			ChatID:        chatId,
			Pause:         false,
			LogRecipients: []int64{},
			ChatName:      name,
			ChatUsername:  username,
		}
		accessHash, err := client.GetAccessHash(ctx, chatSettings.ChatID)
		if err != nil {
			log.Printf("[getChatSettings] GetAccessHash failed for chatID=%d: %v", chatSettings.ChatID, err)
		}
		chatSettings.ChatAccessHash = accessHash
		writeChatSettings(ctx, chatId, chatSettings)
	}
	return chatSettings
}

func checkForDuplicates(ctx context.Context, chatId int64, userid int64, b *bot.Bot, update *models.Update) bool {
	// Check for duplicates
	sessionsMux.Lock()
	defer sessionsMux.Unlock()

	chatSessions, ok := sessions[chatId]
	if !ok {
		sessions[chatId] = map[int64]*BanInfo{}
		chatSessions = sessions[chatId]
	}

	for responseMessage, messageSession := range chatSessions {
		if messageSession.UserID != userid {
			continue
		}
		systemAnswerToMessage(ctx, b, chatId, update.Message.ID, fmt.Sprintf("[Голосование уже создано](tg://privatepost?channel=%s&post=%d)", makePublicGroupString(chatId), responseMessage))
		return true
	}

	return false
}

func makeVoteMessage(ctx context.Context, banInfo *BanInfo, b *bot.Bot) bool {
	// голосуем за бан @пользователя необходимо Н голосов
	//  Последнее сообщение: тут текст

	responseMessage, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    banInfo.ChatID,
		Text:      banInfo.BanMessage,
		ParseMode: models.ParseModeMarkdown,
		ReplyParameters: &models.ReplyParameters{
			ChatID:    banInfo.ChatID,
			MessageID: int(banInfo.RequestMessageID),
		},
		ReplyMarkup: getVoteButtons(0, 0, banInfo.Type),
		LinkPreviewOptions: &models.LinkPreviewOptions{
			IsDisabled: bot.True(),
		},
	})
	if err != nil {
		log.Printf("[makeVoteMessage] SendMessage failed: userID=%d chatID=%d type=%d: %v", banInfo.UserID, banInfo.ChatID, banInfo.Type, err)
		return false
	}
	log.Printf("[makeVoteMessage] vote message sent: messageID=%d chatID=%d userID=%d", responseMessage.ID, banInfo.ChatID, banInfo.UserID)

	sessionsMux.Lock()
	defer sessionsMux.Unlock()

	chatSessions, ok := sessions[banInfo.ChatID]
	if !ok {
		sessions[banInfo.ChatID] = map[int64]*BanInfo{}
		chatSessions = sessions[banInfo.ChatID]
	}
	banInfo.Voters = map[int64]int8{}
	banInfo.VoteMessageID = int64(responseMessage.ID)
	chatSessions[int64(responseMessage.ID)] = banInfo
	return true
}

func onPauseMessage(ctx context.Context, b *bot.Bot, message *models.Message) {
	systemAnswerToMessage(ctx, b, message.Chat.ID, message.ID, fmt.Sprintf("[%s %s](tg://user?id=%d), бот в данный момент приостановлен", message.From.FirstName, message.From.LastName, message.From.ID))
}

func voteCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	answer := ANSWER_SOMETHING_WRONG
	defer func() {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
			ShowAlert:       false,
			Text:            answer,
		})
	}()

	if update.CallbackQuery == nil || update.CallbackQuery.Message.Message == nil {
		return
	}

	sessionsMux.Lock()
	defer sessionsMux.Unlock()

	s, chatSession, superPoke, ok := parseVoteSession(ctx, b, update)
	if !ok {
		return
	}

	if s.OwnerID == update.CallbackQuery.From.ID && superPoke == 0 {
		log.Printf("[voteCallbackHandler] userID=%d attempted to vote on their own poll in chatID=%d", update.CallbackQuery.From.ID, s.ChatID)
		answer = ANSWER_OWN
		return
	}

	answer = handleVote(ctx, b, update, s, chatSession, superPoke)
}

// parseVoteSession looks up the active BanInfo session for the incoming callback.
// Caller must hold sessionsMux.
func parseVoteSession(ctx context.Context, b *bot.Bot, update *models.Update) (s *BanInfo, chatSession map[int64]*BanInfo, superPoke int, ok bool) {
	msg := update.CallbackQuery.Message.Message
	log.Printf("[voteCallbackHandler] vote=%q messageID=%d chatID=%d userID=%d",
		update.CallbackQuery.Data, msg.ID, msg.Chat.ID, update.CallbackQuery.From.ID)

	chatSession, ok = sessions[msg.Chat.ID]
	if !ok {
		log.Printf("[voteCallbackHandler] no active session for chatID=%d", msg.Chat.ID)
		return nil, nil, 0, false
	}
	s, ok = chatSession[int64(msg.ID)]
	if !ok {
		log.Printf("[voteCallbackHandler] no session for messageID=%d in chatID=%d, deleting stale vote message", msg.ID, msg.Chat.ID)
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: msg.Chat.ID, MessageID: msg.ID})
		return nil, nil, 0, false
	}

	adminsMux.Lock()
	isAdmin, isInAdminList := checkAdmins(ctx, b, s.ChatID)[update.CallbackQuery.From.ID]
	if isInAdminList && isAdmin {
		superPoke = 1
		if update.CallbackQuery.Data == "button_downvote" {
			superPoke = -1
		}
	}
	adminsMux.Unlock()

	return s, chatSession, superPoke, true
}

// handleVote records the vote, tallies the result, and dispatches the outcome.
// Returns the answer message to send back to the voter.
// Caller must hold sessionsMux.
func handleVote(ctx context.Context, b *bot.Bot, update *models.Update, s *BanInfo, chatSession map[int64]*BanInfo, superPoke int) string {
	isDownvote := update.CallbackQuery.Data == "button_downvote"
	log.Printf("[handleVote] userID=%d chatID=%d type=%d superPoke=%d isDownvote=%v voters=%d score=%d",
		update.CallbackQuery.From.ID, s.ChatID, s.Type, superPoke, isDownvote, len(s.Voters), s.Score)

	voteResult := 1
	answer := ANSWER_BAN
	if s.Type != BAN {
		answer = ANSWER_MUTE
	}
	if isDownvote {
		voteResult = -1
		answer = ANSWER_NOTBAN
		if s.Type != BAN {
			answer = ANSWER_NOTMUTE
		}
	}

	s.Voters[update.CallbackQuery.From.ID] = int8(voteResult)

	upvotes, downvotes := 0, 0
	for _, v := range s.Voters {
		if v == 1 {
			upvotes++
		} else {
			downvotes++
		}
	}

	msgID := int64(update.CallbackQuery.Message.Message.ID)

	if superPoke == 1 || upvotes-downvotes >= int(s.Score) {
		switch s.Type {
		case BAN:
			banUser(ctx, b, s)
		case MUTE:
			muteUser(ctx, b, s)
		case TEXT_ONLY:
			textOnlyUser(ctx, b, s)
		}
		delete(chatSession, msgID)
		return answer
	}

	if superPoke == -1 || downvotes-upvotes >= int(MID_SCORE) {
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: s.ChatID, MessageID: int(s.VoteMessageID)})
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: s.ChatID, MessageID: int(s.RequestMessageID)})
		delete(chatSession, msgID)
		return answer
	}

	b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
		MessageID:   update.CallbackQuery.Message.Message.ID,
		ReplyMarkup: getVoteButtons(upvotes, downvotes, s.Type),
	})

	return answer
}

func checkAdmins(ctx context.Context, b *bot.Bot, chatID int64) (chatAdmins map[int64]bool) {
	chatAdmins, rep := admins[chatID]
	if !rep {
		var err error
		chatAdmins, err = getAdmins(ctx, b, chatID)
		if err != nil {
			return make(map[int64]bool)
		}
		admins[chatID] = chatAdmins
	}
	return chatAdmins
}

func pauseHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	// CHECK
	adminsMux.Lock()
	chatAdmins := checkAdmins(ctx, b, update.Message.Chat.ID)
	_, rep := chatAdmins[update.Message.From.ID]
	adminsMux.Unlock()

	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    update.Message.Chat.ID,
		MessageID: update.Message.ID,
	})
	if !rep {
		return
	}
	log.Printf("[pauseHandler] userID=%d in chatID=%d: %q", update.Message.From.ID, update.Message.Chat.ID, update.Message.Text)

	settingsMux.Lock()
	chatSettings := getChatSettings(ctx, update.Message.Chat.ID)
	var message string
	if strings.Contains(update.Message.Text, "enable") {
		chatSettings.Pause = true
		message = "Режим паузы активирован"
	} else {
		chatSettings.Pause = false
		message = "Режим паузы деактивирован"
	}
	writeChatSettings(ctx, update.Message.Chat.ID, chatSettings)
	settingsMux.Unlock()

	replay, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   message,
	})
	if err != nil {
		return
	}
	go delay(ctx, 10, func() {
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    replay.Chat.ID,
			MessageID: replay.ID,
		})
	})

}

func handler(ctx context.Context, b *bot.Bot, update *models.Update) {

}

func actionCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
		ShowAlert:       false,
	})
	// log.Println(len(update.CallbackQuery.Data))
	data, err := unmarshal(update.CallbackQuery.Data[2:])
	if err != nil {
		log.Printf("[actionCallbackHandler] unmarshal failed for callbackData=%q: %v", update.CallbackQuery.Data, err)
		return
	}

	switch data.Action {
	case ACTION_UNBAN:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[DATA_TYPE_USERID]
			if !ok {
				log.Printf("[actionCallbackHandler] ACTION_UNBAN: missing userID in callback data, chatID=%d", data.ChatID)
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.CallbackQuery.From.ID,
					Text:   "Не удалось разблокировать пользователя",
					ReplyParameters: &models.ReplyParameters{
						ChatID:    update.CallbackQuery.From.ID,
						MessageID: update.CallbackQuery.Message.Message.ID,
					},
				})
				return
			}
			ok, err := unbanUser(ctx, b, data.ChatID, getInt(userIdRaw))
			if err != nil {
				log.Printf("[actionCallbackHandler] ACTION_UNBAN: unbanUser failed for chatID=%d: %v", data.ChatID, err)
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.CallbackQuery.From.ID,
					Text:   "Не удалось разблокировать пользователя",
					ReplyParameters: &models.ReplyParameters{
						ChatID:    update.CallbackQuery.From.ID,
						MessageID: update.CallbackQuery.Message.Message.ID,
					},
				})
				return
			}
			if !ok {
				log.Printf("[actionCallbackHandler] ACTION_UNBAN: unban returned false for chatID=%d", data.ChatID)
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.CallbackQuery.From.ID,
					Text:   "Не удалось разблокировать пользователя",
					ReplyParameters: &models.ReplyParameters{
						ChatID:    update.CallbackQuery.From.ID,
						MessageID: update.CallbackQuery.Message.Message.ID,
					},
				})
				return
			}
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.CallbackQuery.From.ID,
				Text:   "Пользователь разблокирован",
				ReplyParameters: &models.ReplyParameters{
					ChatID:    update.CallbackQuery.From.ID,
					MessageID: update.CallbackQuery.Message.Message.ID,
				},
			})
		}
	case ACTION_DELETE_ALL:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[DATA_TYPE_USERID]
			if !ok {
				log.Printf("[actionCallbackHandler] ACTION_DELETE_ALL: missing userID in callback data, chatID=%d", data.ChatID)
				return
			}
			ok, err := deleteAllMessages(ctx, b, data.ChatID, getInt(userIdRaw))
			if err != nil {
				log.Printf("[actionCallbackHandler] ACTION_DELETE_ALL: deleteAllMessages failed for chatID=%d: %v", data.ChatID, err)
				return
			}
			if !ok {
				log.Printf("[actionCallbackHandler] ACTION_DELETE_ALL: deleteAllMessages returned false for chatID=%d", data.ChatID)
				return
			}

		}
	case ACTION_SHOW_CHAT_LIST:
		{
			log.Printf("[actionCallbackHandler] ACTION_SHOW_CHAT_LIST: userID=%d", update.CallbackQuery.From.ID)
			chats := getChatsForAdmin(ctx, b, update.CallbackQuery.From.ID)
			log.Printf("[actionCallbackHandler] ACTION_SHOW_CHAT_LIST: found %d chats for userID=%d", len(chats), update.CallbackQuery.From.ID)
			_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        "Control panel",
				ReplyMarkup: getChatListKeyboard(chats),
			})
			if err != nil {
				log.Printf("[actionCallbackHandler] ACTION_SHOW_CHAT_LIST: EditMessageText failed: %v", err)
			}
		}
	case ACTION_SHOW_CHAT_ID:
		{
			log.Printf("[actionCallbackHandler] ACTION_SHOW_CHAT_ID: chatID=%d userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			chatName := getChatName(ctx, b, data.ChatID)
			b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        fmt.Sprintf("Управление чатом: %s", chatName),
				ReplyMarkup: getChatActionsKeyboard(data.ChatID),
			})
		}
	case ACTION_PAUSE_CHAT:
		{
			log.Printf("[actionCallbackHandler] ACTION_PAUSE_CHAT: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}

			settingsMux.Lock()
			chatSettings := getChatSettings(ctx, data.ChatID)
			chatSettings.Pause = true
			settings[data.ChatID] = chatSettings
			writeChatSettings(ctx, data.ChatID, chatSettings)
			settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Режим паузы активирован", false)
		}
	case ACTION_UNPAUSE_CHAT:
		{
			log.Printf("[actionCallbackHandler] ACTION_UNPAUSE_CHAT: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}

			settingsMux.Lock()
			chatSettings := getChatSettings(ctx, data.ChatID)
			chatSettings.Pause = false
			settings[data.ChatID] = chatSettings
			writeChatSettings(ctx, data.ChatID, chatSettings)
			settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Режим паузы деактивирован", false)
		}
	case ACTION_ENABLED_LOG:
		{
			log.Printf("[actionCallbackHandler] ACTION_ENABLED_LOG: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userID := update.CallbackQuery.From.ID

			settingsMux.Lock()
			chatSettings := getChatSettings(ctx, data.ChatID)
			if slices.Contains(chatSettings.LogRecipients, userID) {
				settingsMux.Unlock()
				systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы уже в списке на получение отчётов", false)
				return
			}
			chatSettings.LogRecipients = append(chatSettings.LogRecipients, userID)
			settings[data.ChatID] = chatSettings
			writeChatSettings(ctx, data.ChatID, chatSettings)
			settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы добавлены в список на получение отчётов", false)
		}
	case ACTION_DISABLED_LOG:
		{
			log.Printf("[actionCallbackHandler] ACTION_DISABLED_LOG: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userID := update.CallbackQuery.From.ID

			settingsMux.Lock()
			chatSettings := getChatSettings(ctx, data.ChatID)
			index := slices.Index(chatSettings.LogRecipients, userID)
			if index == -1 {
				settingsMux.Unlock()
				systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы не состоите в списке получателей отчётов", false)
				return
			}
			chatSettings.LogRecipients = slices.Delete(chatSettings.LogRecipients, index, index+1)
			settings[data.ChatID] = chatSettings
			writeChatSettings(ctx, data.ChatID, chatSettings)
			settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы удалены из списка получателей отчётов", false)

		}
	case ACTION_UNMUTE:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[DATA_TYPE_USERID]
			if !ok {
				log.Printf("[actionCallbackHandler] ACTION_UNMUTE: missing userID in callback data, chatID=%d", data.ChatID)
				return
			}
			log.Printf("[actionCallbackHandler] ACTION_UNMUTE: userID=%d chatID=%d by userID=%d", getInt(userIdRaw), data.ChatID, update.CallbackQuery.From.ID)

			ok, err := unmuteUser(ctx, b, data.ChatID, getInt(userIdRaw))
			if err != nil {
				log.Printf("[actionCallbackHandler] ACTION_UNMUTE: unmuteUser failed for chatID=%d: %v", data.ChatID, err)
				return
			}
			if !ok {
				log.Printf("[actionCallbackHandler] ACTION_UNMUTE: unmute returned false for chatID=%d", data.ChatID)
				return
			}
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.CallbackQuery.From.ID,
				Text:   "Ограничения пользователя сняты",
				ReplyParameters: &models.ReplyParameters{
					ChatID:    update.CallbackQuery.From.ID,
					MessageID: update.CallbackQuery.Message.Message.ID,
				},
			})

		}
	case ACTION_LEAVE_CHAT:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.Chat.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			log.Printf("[actionCallbackHandler] ACTION_LEAVE_CHAT: chatID=%d by userID=%d", data.ChatID, update.CallbackQuery.From.ID)
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: data.ChatID,
				Text:   "Покидаю чат. До свидания!",
			})
			_, err := b.LeaveChat(ctx, &bot.LeaveChatParams{
				ChatID: data.ChatID,
			})
			if err != nil {
				log.Printf("Can't leave chat %d : %v", data.ChatID, err)
				return
			}

			// return to list of chats
			chats := getChatsForAdmin(ctx, b, update.CallbackQuery.From.ID)
			log.Printf("[actionCallbackHandler] ACTION_LEAVE_CHAT: refreshed %d chats for userID=%d", len(chats), update.CallbackQuery.From.ID)
			b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        "Control panel",
				ReplyMarkup: getChatListKeyboard(chats),
			})

		}
	}
}

func testHandler(ctx context.Context, b *bot.Bot, update *models.Update) {

	log.Printf("[testHandler] chatID=%d userID=%d", update.Message.Chat.ID, update.Message.From.ID)
	// chatId := update.Message.Chat.ID
	// publicInt, _ := strconv.ParseInt(makePublicGroupString(chatId), 10, 64)

	jcart, _ := json.MarshalIndent(update, "", "\t")
	fmt.Println(string(jcart))

	// chatinfo, err := b.GetChatAdministrators(ctx, &bot.GetChatAdministratorsParams{
	// 	ChatID: update.Message.Chat.ID,
	// })

	// if err == nil {
	// 	jcart, _ := json.MarshalIndent(chatinfo, "", "\t")
	// 	log.Println(string(jcart))
	// 	return
	// }

	// testData := &Item{
	// 	Action: 1,
	// 	ChatID: publicInt,
	// 	Data:   map[uint8]interface{}{1: 23, 4: 4342},

	// 	// Data: "test",
	// }

	// enc, err := marshal(testData)
	// if err != nil {
	// 	log.Printf("Shit happens %v", err)
	// 	return
	// }

	// log.Printf("Button data %s, length %d\n", enc, len(enc))

	// _, err = b.SendMessage(ctx, &bot.SendMessageParams{
	// 	ChatID:    chatId,
	// 	Text:      "Test test",
	// 	ParseMode: models.ParseModeMarkdown,
	// 	ReplyMarkup: &models.InlineKeyboardMarkup{
	// 		InlineKeyboard: [][]models.InlineKeyboardButton{
	// 			{
	// 				{Text: "testbtn", CallbackData: fmt.Sprintf("b_%s", enc)},
	// 			},
	// 		},
	// 	},
	// })
	// if err != nil {
	// 	log.Println(err)
	// }
}

// TODO: have to be a better func
func getChatsForAdmin(ctx context.Context, b *bot.Bot, userID int64) []Chat {
	chats := make([]Chat, 0, 4)

	adminsMux.Lock()
	defer adminsMux.Unlock()

	for k, v := range admins {
		_, ok := v[userID]
		if !ok && userID != superAdminID {
			continue
		}
		name := getChatName(ctx, b, k)
		chats = append(chats, Chat{
			ChatID:   k,
			ChatName: name,
		})
	}
	return chats
}

func startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID

	if chatID == userID {
		// admin menu
		chats := getChatsForAdmin(ctx, b, userID)
		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        "Control panel",
			ParseMode:   models.ParseModeMarkdown,
			ReplyMarkup: getChatListKeyboard(chats),
		})
		if err != nil {
			log.Println(err)
		}
		return
	}
	// chat menu
	return

}

func getChatNameFromSettings(chatID int64) string {
	settingsMux.Lock()
	defer settingsMux.Unlock()

	var name string
	setting, ok := settings[chatID]
	if ok {
		name = setting.ChatName
	}

	return name
}

type cacheEntity[K comparable, V any] struct {
	cache  cache.Cache[K, V]
	cancel context.CancelFunc
}

func cacheBanInfo(chatID int64, userID int64) {
	chatCache, ok := banCache[chatID]
	if !ok {
		chatCache = newCache()

		banCache[chatID] = chatCache
	}

	chatCache.cache.Set(userID, struct{}{}, time.Minute*30)
}

func getCachedBanInfo(chatID int64, userID int64) bool {
	chatCache, ok := banCache[chatID]
	if !ok {
		chatCache = newCache()

		banCache[chatID] = chatCache
		return false
	}

	_, ok = chatCache.cache.Get(userID)
	return ok
}

func newCache() *cacheEntity[int64, struct{}] {
	ctx, cancel := context.WithCancel(context.Background())
	chatCache := &cacheEntity[int64, struct{}]{
		cancel: cancel,
		cache:  *cache.New[int64, struct{}](ctx),
	}

	go func() {
		intCh := make(chan os.Signal, 1)
		signal.Notify(intCh, os.Interrupt, os.Kill, syscall.SIGTERM)

		select {
		case <-intCh:
			cancel()
		case <-ctx.Done():
			cancel()
		}
	}()

	return chatCache
}

func deleteMessageHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	adminsMux.Lock()
	chatAdmins := checkAdmins(ctx, b, update.Message.Chat.ID)
	_, rep := chatAdmins[update.Message.From.ID]
	adminsMux.Unlock()

	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    update.Message.Chat.ID,
		MessageID: update.Message.ID,
	})
	if !rep {
		return
	}
	chatId := update.Message.Chat.ID
	for _, v := range update.Message.Entities {
		if v.Type == models.MessageEntityTypeURL {
			log.Printf("[deleteMessageHandler] processing URL entity in chatID=%d: %q", chatId, update.Message.Text[v.Offset:v.Offset+v.Length])
			rxResult := linkRegex.FindAllStringSubmatch(update.Message.Text[v.Offset:v.Offset+v.Length], -1)
			for i := range rxResult {
				if rxResult[i][1] == "" {
					linkUsername := rxResult[i][2]
					if linkUsername != update.Message.Chat.Username {
						log.Printf("[deleteMessageHandler] link chat username mismatch: got %q expected %q in chatID=%d", linkUsername, update.Message.Chat.Username, chatId)
						continue
					}
				} else {
					parsedID, _ := strconv.ParseInt("-100"+rxResult[i][2], 10, 64)
					if chatId != parsedID {
						log.Printf("[deleteMessageHandler] link chatID mismatch: got %d expected %d", parsedID, chatId)
						continue
					}
				}
				pokeMessageID, err := strconv.ParseInt(rxResult[i][3], 10, 64)
				if err != nil {
					log.Printf("[deleteMessageHandler] corrupted messageID %q in URL entity chatID=%d", rxResult[i][3], chatId)
					continue
				}
				b.DeleteMessage(ctx, &bot.DeleteMessageParams{
					ChatID:    chatId,
					MessageID: int(pokeMessageID),
				})
			}
		}

	}
}
