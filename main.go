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

	mdRegex *regexp.Regexp = regexp.MustCompile(`(['_~>#!=\-])`)

	sessionsMux sync.Mutex
	sessions    map[int64]map[int64]*BanInfo = make(map[int64]map[int64]*BanInfo)

	settingsMux sync.Mutex
	settings    map[int64]*DyncmicSetting

	adminsMux sync.Mutex
	admins    map[int64]map[int64]bool
	banCache  map[int64]*cacheEntity[int64, struct{}] = make(map[int64]*cacheEntity[int64, struct{}])

	superAdminID int64

	ANSWER_OWN             string = "Нельзя голосовать за свою голосовалку"
	ANSWER_NOTBAN          string = "Против бана. Голос учтён"
	ANSWER_BAN             string = "За бан. Голос учтён"
	ANSWER_MUTE            string = "За мут. Голос учтён"
	ANSWER_NOTMUTE         string = "Против мута. Голос учтён"
	ANSWER_SOMETHING_WRONG string = "что то пошло не так"
)

// go dealy(ctx, 10, func() { log.Printf("Delayed call") })
func dealy(ctx context.Context, delaySeconds int64, arg func()) {
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

func escape(line string) string {
	first_step := regexp.QuoteMeta(line)
	second_step := strings.ReplaceAll(first_step, "`", "\\`")
	return mdRegex.ReplaceAllString(second_step, "\\$1")
}

func main() {
	var err error
	// TODO: replace defaul logger
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
		log.Panicf("ADMIN_ID error parsing: %v", err)
	}

	// // Grab those from https://my.telegram.org/apps.
	// appID := flag.Int("api-id", 0, "app id")
	// appHash := flag.String("api-hash", "hash", "app hash")
	// // Get it from bot father.

	appIdString, ok := os.LookupEnv("BOT_APP_ID")
	if !ok {
		log.Panic("BOT_APP_ID have to be set")
	}
	appId, err := strconv.ParseInt(appIdString, 10, 32)
	if err != nil {
		log.Panicf("Can't parse appId: %v", err)
	}

	appHash, ok := os.LookupEnv("BOT_APP_HASH")
	if !ok {
		log.Panic("BOT_APP_HASH")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	initDb(ctx, mongoAddr, dbName)
	settings = readChatsSettings(ctx)

	client = &mtproto.MTProtoHelper{AppId: int(appId), AppHash: appHash, BotApiKey: botApiKey, Logger: logger}
	if err = client.Init(ctx); err != nil {
		log.Panicf("Can't init mtproto: %v", err)
	}
	defer client.Stop()

	for _, chatSettings := range settings {
		if chatSettings.ChatAccessHash != 0 {
			continue
		}
		accessHash, err := client.GetAccessHash(ctx, chatSettings.ChatID)
		if err != nil {
			log.Printf("Can't get accessHash for chatID %v: %v", chatSettings.ChatID, err)
		}
		chatSettings.ChatAccessHash = accessHash
		writeChatSettings(ctx, chatSettings.ChatID, chatSettings)
		log.Printf("Updated the access hash for a chat %v", chatSettings.ChatName)
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
	log.Printf("Starting %s", me.Username)
	// each 12 hours update admins list
	go ticker(ctx, 43200, getChatAdmins)
	myBot.Start(ctx)
}

func botRemovedFromChat(ctx context.Context, chatID int64) {
	settingsMux.Lock()
	chatSettings, ok := settings[chatID]
	if !ok {
		chatSettings = &DyncmicSetting{ChatName: "unknown name", ChatUsername: "unknown username"}
	}

	name := chatSettings.ChatName
	username := chatSettings.ChatUsername
	if ok {
		delete(settings, chatID)
	}
	settingsMux.Unlock()

	deleteChatSettings(ctx, chatID)
	adminsMux.Lock()
	_, ok = admins[chatID]
	if ok {
		delete(admins, chatID)
	}
	adminsMux.Unlock()

	sessionsMux.Lock()
	delete(banCache, chatID)
	sessionsMux.Unlock()

	myBot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: 198082233,
		Text:   fmt.Sprintf("Bot deleted from chat \"%s\" @%s %d", name, username, chatID),
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
					Text:   fmt.Sprintf("Bot added to chat \"%s\" @%s", update.MyChatMember.Chat.Title, update.MyChatMember.Chat.Username),
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

func getChatSettings(ctx context.Context, chatId int64) (chatSettings *DyncmicSetting) {
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
		chatSettings = &DyncmicSetting{
			ChatID:        chatId,
			Pause:         false,
			LogRecipients: []int64{},
			ChatName:      name,
			ChatUsername:  username,
		}
		accessHash, err := client.GetAccessHash(ctx, chatSettings.ChatID)
		if err != nil {
			log.Printf("Can't get accessHash for chatID %v: %v", chatSettings.ChatID, err)
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

	for responceMessage, messageSession := range chatSessions {
		if messageSession.UserID != userid {
			continue
		}
		systemAnswerToMessage(ctx, b, chatId, update.Message.ID, fmt.Sprintf("[Уже есть голосовалка](tg://privatepost?channel=%s&post=%d)", makePublicGroupString(chatId), responceMessage))
		return true
	}

	return false
}

func makeVoteMessage(ctx context.Context, banInfo *BanInfo, b *bot.Bot) bool {
	// голосуем за бан @пользователя необходимо Н голосов
	//  Последнее сообщение: тут текст

	responceMessage, err := b.SendMessage(ctx, &bot.SendMessageParams{
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
		log.Printf("Can't send ban message %s \nError: %v", banInfo.BanMessage, err)
		return false
	}
	log.Printf("makeVoteMessage: Responce id %d\n", responceMessage.ID)

	sessionsMux.Lock()
	defer sessionsMux.Unlock()

	chatSessions, ok := sessions[banInfo.ChatID]
	if !ok {
		sessions[banInfo.ChatID] = map[int64]*BanInfo{}
		chatSessions = sessions[banInfo.ChatID]
	}
	banInfo.Voiters = map[int64]int8{}
	banInfo.VoteMessageID = int64(responceMessage.ID)
	chatSessions[int64(responceMessage.ID)] = banInfo
	return true
}

func onPauseMessage(ctx context.Context, b *bot.Bot, message *models.Message) {
	systemAnswerToMessage(ctx, b, message.Chat.ID, message.ID, fmt.Sprintf("[%s %s](tg://user?id=%d), бот на паузе", message.From.FirstName, message.From.LastName, message.From.ID))
}

func voteCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	var answer_message *string
	answer_message = &ANSWER_SOMETHING_WRONG
	defer func() {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
			ShowAlert:       false,
			Text:            *answer_message,
		})
	}()
	if update.CallbackQuery == nil || update.CallbackQuery.Message.Message == nil {
		// already deleted message
		return
	}
	log.Printf("Get vote %s, from message id: %d chatid: %d", update.CallbackQuery.Data, update.CallbackQuery.Message.Message.ID, update.CallbackQuery.Message.Message.Chat.ID)

	sessionsMux.Lock()
	defer sessionsMux.Unlock()

	chatSession, ok := sessions[update.CallbackQuery.Message.Message.Chat.ID]
	if !ok {
		log.Printf("something goes wrong, there are no session for chatID: %d", update.CallbackQuery.Message.Message.Chat.ID)
		return
	}
	s, ok := chatSession[int64(update.CallbackQuery.Message.Message.ID)]
	if !ok {
		log.Printf("something goes wrong, there are no message with ID: %d", update.CallbackQuery.Message.Message.ID)
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: update.CallbackQuery.Message.Message.Chat.ID, MessageID: update.CallbackQuery.Message.Message.ID})
		return
	}

	superPoke := 0

	adminsMux.Lock()
	isAdmin, isInAdminList := checkAdmins(ctx, b, s.ChatID)[update.CallbackQuery.From.ID]
	if isInAdminList && isAdmin {
		superPoke = 1
		if update.CallbackQuery.Data == "button_downvote" {
			superPoke = -1
		}
	}
	adminsMux.Unlock()

	if s.OwnerID == update.CallbackQuery.From.ID && superPoke == 0 {
		log.Println("try to vote to it's own")
		answer_message = &ANSWER_OWN
		return
	}
	voteResult := 1
	if s.Type == BAN {
		answer_message = &ANSWER_BAN
	} else {
		answer_message = &ANSWER_MUTE
	}

	if update.CallbackQuery.Data == "button_downvote" {
		if s.Type == BAN {
			answer_message = &ANSWER_NOTBAN
		} else {
			answer_message = &ANSWER_NOTMUTE
		}

		voteResult = -1
	}
	s.Voiters[update.CallbackQuery.From.ID] = int8(voteResult)

	upvoteCount := 0
	downvoteCount := 0
	for _, v := range s.Voiters {
		if v == 1 {
			upvoteCount = upvoteCount + 1
		} else {
			downvoteCount = downvoteCount + 1
		}
	}

	if superPoke == 1 || (upvoteCount-downvoteCount >= int(s.Score)) {
		//move to separate function
		switch s.Type {
		case BAN:
			{
				// TODO: move to separate corutine
				banUser(ctx, b, s)
			}
		case MUTE:
			{
				muteUser(ctx, b, s)
			}
		case TEXT_ONLY:
			{
				textOnlyUser(ctx, b, s)
			}
		}

		delete(chatSession, int64(update.CallbackQuery.Message.Message.ID))
		return
	}
	// Downvoted
	if superPoke == -1 || (downvoteCount-upvoteCount >= int(MID_SCORE)) {
		//Delete the vote message
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    s.ChatID,
			MessageID: int(s.VoteMessageID),
		})
		// Delete the vote request
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    s.ChatID,
			MessageID: int(s.RequestMessageID),
		})
		delete(chatSession, int64(update.CallbackQuery.Message.Message.ID))
		// ToDo: prepare report
		// delete user and user's messages
		return
	}

	b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
		MessageID:   update.CallbackQuery.Message.Message.ID,
		ReplyMarkup: getVoteButtons(upvoteCount, downvoteCount, s.Type),
	})

}

func checkAdmins(ctx context.Context, b *bot.Bot, chatID int64) (chatAdmins map[int64]bool) {
	chatAdmins, rep := admins[chatID]
	if !rep {
		chatAdmins, err := getAdmins(ctx, b, chatID)
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
	log.Printf("Pause handler: %s", update.Message.Text)

	settingsMux.Lock()
	chatSettings := getChatSettings(ctx, update.Message.Chat.ID)
	var message string
	if strings.Contains(update.Message.Text, "enable") {
		chatSettings.Pause = true
		message = "Пауза активированна"
	} else {
		chatSettings.Pause = false
		message = "Пауза выключенна"
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
	go dealy(ctx, 10, func() {
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    replay.Chat.ID,
			MessageID: replay.ID,
		})
	})

}

func handler(ctx context.Context, b *bot.Bot, update *models.Update) {

}

func banHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	// log.Printf("Whole message %v", update.Message.)
	pokeConter := 0

	chatId := update.Message.Chat.ID

	settingsMux.Lock()
	chatSettings := getChatSettings(ctx, chatId)
	senderId := update.Message.From.ID
	if update.Message.SenderChat != nil {
		senderId = update.Message.SenderChat.ID
	}
	if chatSettings.Pause {
		settingsMux.Unlock()
		onPauseMessage(ctx, b, update.Message)
		return
	}
	settingsMux.Unlock()

	if len(update.Message.Entities) == 1 {
		systemAnswerToMessage(ctx, b, chatId, update.Message.ID, escape(fmt.Sprintf("Для использования бота необходимо указать ему ссылку на сообщение или указать пользователя\nНапример:\n@%s https://t.me/c/1657123097/2854347\n/ban https://t.me/c/1657123097/2854347\n/ban @username", myID)))
	}

	for _, v := range update.Message.Entities {
		log.Printf("Message type: %v, entities %s", v.Type, update.Message.Text[v.Offset:v.Offset+v.Length])
		var err error
		var banInfo *BanInfo
		if v.Type == models.MessageEntityTypeTextMention {
			log.Printf("Raw data %v ,%v %v", v.Type, v.User.ID, v.User.FirstName)
			banInfo, err = getBanInfoByUserID(ctx, chatId, v.User.ID)
			if err != nil {
				log.Printf("TODO: return error to user! %v", err)
				continue
			}
		}
		if v.Type == models.MessageEntityTypeMention {
			username := update.Message.Text[v.Offset+1 : v.Offset+v.Length]
			log.Printf("mention username @%s", username)
			if username == myID {
				continue
			}
			banInfo, err = getBanInfoByUsername(ctx, chatId, username)
			if err != nil {
				log.Printf("TODO: return error to user! %v", err)
				userInfo, err := client.GetUserByUsername(ctx, username)
				if err != nil {
					systemAnswerToMessage(ctx, b, chatId, update.Message.ID, fmt.Sprintf("Извините пользователь с ником %v не найден", username), true)
					continue
				}
				//
				banInfo = getBanInfoByUserIDNoDB(chatId, userInfo.UserId, userInfo.Username)
			}
		}
		if v.Type == models.MessageEntityTypeURL {
			log.Printf("%v %s", v, v.URL)
			rxResult := linkRegex.FindAllStringSubmatch(update.Message.Text[v.Offset:v.Offset+v.Length], -1)
			for i := range rxResult {
				if rxResult[i][1] == "" {
					linkUsername := rxResult[i][2]
					if linkUsername != update.Message.Chat.Username {
						log.Printf("Chat Username is not match %s != %s\n", linkUsername, update.Message.Chat.Username)
						continue
					}
				} else {
					parsedID, _ := strconv.ParseInt("-100"+rxResult[i][2], 10, 64)
					if chatId != parsedID {
						log.Printf("Chat ID is not match %d != %d\n", parsedID, chatId)
						continue
					}
				}
				pokeMessageID, err := strconv.ParseInt(rxResult[i][3], 10, 64)
				if err != nil {
					log.Printf("Message ID is curropted %s", rxResult[i][3])
					continue
				}
				banInfo, err = getBanInfo(ctx, chatId, pokeMessageID)
				if err != nil {
					systemAnswerToMessage(ctx, b, chatId, update.Message.ID, "Извините сообщение не найдено, исользуйте альтернативный метод через \"/ban @username\"", true)
					continue
				}
				banInfo.TargetMessageID = pokeMessageID
			}
		}
		if banInfo == nil {
			continue
		}
		banInfo.OwnerID = senderId
		// if used a chat alias should be saved proper ID
		banInfo.RequestMessageID = int64(update.Message.ID)
		if checkForDuplicates(ctx, chatId, banInfo.UserID, b, update) {
			continue
		}

		sessionsMux.Lock()
		cached := getCachedBanInfo(banInfo.ChatID, banInfo.UserID)
		sessionsMux.Unlock()

		if cached {
			systemAnswerToMessage(ctx, b, chatId, update.Message.ID, "Этот пользователь уже был недавно забанен", true)
			continue
		}

		log.Printf("Start vote process score for user %d %d", banInfo.UserID, banInfo.Score)
		if !makeVoteMessage(ctx, banInfo, b) {
			continue
		}
		pokeConter = pokeConter + 1

	}

	userMakeVote(ctx, senderId, pokeConter)

}

func actionCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
		ShowAlert:       false,
	})
	// log.Println(len(update.CallbackQuery.Data))
	data, err := unmarshal(update.CallbackQuery.Data[2:])
	if err != nil {
		log.Printf("Action is parsed bad %v", err)
		return
	}

	switch data.Action {
	case ACTION_UNBAN:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.From.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[DATA_TYPE_USERID]
			if !ok {
				log.Printf("TODO replace to message to user: err there are no userid")
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.CallbackQuery.From.ID,
					Text:   "Не смог разбанить пользователя",
					ReplyParameters: &models.ReplyParameters{
						ChatID:    update.CallbackQuery.From.ID,
						MessageID: update.CallbackQuery.Message.Message.ID,
					},
				})
				return
			}
			ok, err := unbanUser(ctx, b, data.ChatID, getInt(userIdRaw))
			if err != nil {
				log.Printf("TODO replace to message to user: err %v", err)
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.CallbackQuery.From.ID,
					Text:   "Не смог разбанить пользователя",
					ReplyParameters: &models.ReplyParameters{
						ChatID:    update.CallbackQuery.From.ID,
						MessageID: update.CallbackQuery.Message.Message.ID,
					},
				})
				return
			}
			if !ok {
				log.Printf("TODO replace to message to user: can't unban user")
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.CallbackQuery.From.ID,
					Text:   "Не смог разбанить пользователя",
					ReplyParameters: &models.ReplyParameters{
						ChatID:    update.CallbackQuery.From.ID,
						MessageID: update.CallbackQuery.Message.Message.ID,
					},
				})
				return
			}
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.CallbackQuery.From.ID,
				Text:   "Пользователь разбанен",
				ReplyParameters: &models.ReplyParameters{
					ChatID:    update.CallbackQuery.From.ID,
					MessageID: update.CallbackQuery.Message.Message.ID,
				},
			})
		}
	case ACTION_DELETE_ALL:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.From.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[DATA_TYPE_USERID]
			if !ok {
				log.Printf("TODO replace to message to user: err there are no userid")
				return
			}
			ok, err := deleteAllMessages(ctx, b, data.ChatID, getInt(userIdRaw))
			if err != nil {
				log.Printf("TODO replace to message to user: err %v", err)
				return
			}
			if !ok {
				log.Printf("TODO replace to message to user: can't delete all messages from user")
				return
			}

		}
	case ACTION_SHOW_CHAT_LIST:
		{
			log.Printf("ACTION_SHOW_CHAT_LIST user id %d", update.CallbackQuery.From.ID)
			chats := getChatsForAdmin(ctx, b, update.CallbackQuery.From.ID)
			log.Printf("chats %v", chats)
			_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        "Control pannel",
				ReplyMarkup: getChatListKeyboard(chats),
			})
			if err != nil {
				log.Printf("Can't update message: %v", err)
			}
		}
	case ACTION_SHOW_CHAT_ID:
		{
			log.Printf("ACTION_SHOW_CHAT_ID %d", data.ChatID)
			chatName := getChatName(ctx, b, data.ChatID)
			b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        fmt.Sprintf("Действия для чата %s", chatName),
				ReplyMarkup: getChatActionsKeyboard(data.ChatID),
			})
		}
	case ACTION_PAUSE_CHAT:
		{
			log.Printf("ACTION_PAUSE_CHAT %d", data.ChatID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.From.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}

			settingsMux.Lock()
			chatSettings := getChatSettings(ctx, data.ChatID)
			chatSettings.Pause = true
			settings[data.ChatID] = chatSettings
			writeChatSettings(ctx, data.ChatID, chatSettings)
			settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Пауза активированна", false)
		}
	case ACTION_UNPAUSE_CHAT:
		{
			log.Printf("ACTION_UNPAUSE_CHAT %d", data.ChatID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.From.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}

			settingsMux.Lock()
			chatSettings := getChatSettings(ctx, data.ChatID)
			chatSettings.Pause = false
			settings[data.ChatID] = chatSettings
			writeChatSettings(ctx, data.ChatID, chatSettings)
			settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Пауза деактивированна", false)
		}
	case ACTION_ENABLED_LOG:
		{
			log.Printf("ACTION_ENABLED_LOG %d", data.ChatID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.From.ID, update.CallbackQuery.Message.Message.ID) {
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
			log.Printf("ACTION_DISABLED_LOG %d", data.ChatID)
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.From.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userID := update.CallbackQuery.From.ID

			settingsMux.Lock()
			chatSettings := getChatSettings(ctx, data.ChatID)
			index := slices.Index(chatSettings.LogRecipients, userID)
			if index == -1 {
				settingsMux.Unlock()
				systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы не будете получать отчёты", false)
				return
			}
			chatSettings.LogRecipients = slices.Delete(chatSettings.LogRecipients, index, index+1)
			settings[data.ChatID] = chatSettings
			writeChatSettings(ctx, data.ChatID, chatSettings)
			settingsMux.Unlock()

			systemAnswerToMessage(ctx, b, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.ID, "Вы не будете получать отчёты", false)

		}
	case ACTION_UNMUTE:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.From.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			userIdRaw, ok := data.Data[DATA_TYPE_USERID]
			if !ok {
				log.Printf("TODO replace to message to user: err there are no userid")
				return
			}
			log.Printf("ACTION_UNMUTE userid: %d chatid: %d", userIdRaw, data.ChatID)

			ok, err := unmuteUser(ctx, b, data.ChatID, getInt(userIdRaw))
			if err != nil {
				log.Printf("TODO replace to message to user: err %v", err)
				return
			}
			if !ok {
				log.Printf("TODO replace to message to user: can't unban user")
				return
			}
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.CallbackQuery.From.ID,
				Text:   "Пользователь размьючен",
				ReplyParameters: &models.ReplyParameters{
					ChatID:    update.CallbackQuery.From.ID,
					MessageID: update.CallbackQuery.Message.Message.ID,
				},
			})

		}
	case ACTION_LEAVE_CHAT:
		{
			if !isUserAdmin(ctx, b, data.ChatID, update.CallbackQuery.From.ID, update.CallbackQuery.Message.Message.From.ID, update.CallbackQuery.Message.Message.ID) {
				return
			}
			log.Printf("ACTION_LEAVE_CHAT userid: %d chatid: %d", update.CallbackQuery.Message.Message.From.ID, data.ChatID)
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: data.ChatID,
				Text:   "Bye bye!",
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
			log.Printf("chats %v", chats)
			b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
				MessageID:   update.CallbackQuery.Message.Message.ID,
				Text:        "Control pannel",
				ReplyMarkup: getChatListKeyboard(chats),
			})

		}
	}
}

func testHandler(ctx context.Context, b *bot.Bot, update *models.Update) {

	log.Printf("test handler chatid %d userid %d\n", update.Message.Chat.ID, update.Message.From.ID)
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
			Text:        "Control pannel",
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
