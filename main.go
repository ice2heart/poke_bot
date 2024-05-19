package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/joho/godotenv"
)

const (
	MESSAGE_TTL_DAYS = 14
)

var (
	myBot *bot.Bot

	myID      string
	mainRegex *regexp.Regexp
	linkRegex *regexp.Regexp = regexp.MustCompile(`(?:\s*https://t\.me/(c/)?([\d\w]+)/(\d+))`)

	mdRegex *regexp.Regexp = regexp.MustCompile(`(['_~>#!=\-])`)

	sessions map[int64]map[int64]*BanInfo = make(map[int64]map[int64]*BanInfo)

	settings map[int64]*DyncmicSetting
	admins   map[int64]map[int64]bool
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

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// go ticker(ctx, 5, func() {
	// 	log.Printf("test")
	// })

	// TODO: move to env
	initDb(ctx, mongoAddr, dbName)
	settings = readChatsSettings(ctx)

	opts := []bot.Option{
		bot.WithDefaultHandler(handler),
		// bot.WithDebug(),
		bot.WithMiddlewares(logMessagesMiddleware),
		bot.WithCallbackQueryDataHandler("button", bot.MatchTypePrefix, voteCallbackHandler),
	}

	var err error
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
	for k := range settings {
		admins[k] = getAdmins(ctx, myBot, k)
	}

	mainRegex = regexp.MustCompile(fmt.Sprintf(`%s\s*https://t\.me/(c/)?([\d\w]+)/(\d+)`, myID))
	myBot.RegisterHandlerRegexp(bot.HandlerTypeMessageText, mainRegex, handler_poke)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/pause", bot.MatchTypePrefix, pauseHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/ban", bot.MatchTypePrefix, banHandler)
	myBot.RegisterHandler(bot.HandlerTypeCallbackQueryData, "b_", bot.MatchTypePrefix, actionCallbackHandler)
	myBot.RegisterHandler(bot.HandlerTypeMessageText, "/test", bot.MatchTypePrefix, testHandler)
	log.Printf("Starting %s", me.Username)
	// each 12 hours update admins list
	go ticker(ctx, 720, getChatAdmins)
	myBot.Start(ctx)
}

func getChatAdmins(ctx context.Context) {
	for k := range settings {
		admins[k] = getAdmins(ctx, myBot, k)
		log.Printf("Update admins for chat %d\n%v", k, admins[k])
	}
}

func logMessagesMiddleware(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message != nil {
			// log.Printf("%s say: %s, lang code %s", update.Message.From.FirstName, update.Message.Text, update.Message.From.LanguageCode)
			userPlusOneMessage(ctx, update.Message.From.ID, update.Message.From.Username, fmt.Sprintf("%s %s", update.Message.From.FirstName, update.Message.From.LastName))
			now := time.Now()
			saveMessage(ctx, &ChatMessage{
				MessageID: int64(update.Message.ID),
				ChatID:    update.Message.Chat.ID,
				UserID:    update.Message.From.ID,
				UserName:  update.Message.From.Username,
				Text:      update.Message.Text,
				Date:      uint64(now.AddDate(0, 0, MESSAGE_TTL_DAYS).UnixMilli()),
			})

		}
		next(ctx, b, update)
	}
}

func getChatSettings(ctx context.Context, chatId int64) (chatSettings *DyncmicSetting) {
	chatSettings, prs := settings[chatId]
	if !prs {
		chatSettings = &DyncmicSetting{
			ChatID: chatId,
			Pause:  false,
		}
		writeChatSettings(ctx, chatId, chatSettings)
	}
	return chatSettings
}

func checkForDuplicates(ctx context.Context, chatId int64, userid int64, b *bot.Bot, update *models.Update) bool {
	// Check for duplicates
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
		ReplyMarkup: getVoteButtons(0, 0),
	})
	if err != nil {
		log.Printf("Can't send ban message %s \nError: %v", banInfo.BanMessage, err)
		return false
	}
	log.Printf("Responce id %d\n", responceMessage.ID)

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
	// answering callback query first to let Telegram know that we received the callback query,
	// and we're handling it. Otherwise, Telegram might retry sending the update repetitively
	// as it thinks the callback query doesn't reach to our application. learn more by
	// reading the footnote of the https://core.telegram.org/bots/api#callbackquery type.
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
		ShowAlert:       false,
	})
	log.Printf("Get vote %s, from message id: %d chatid: %d", update.CallbackQuery.Data, update.CallbackQuery.Message.Message.ID, update.CallbackQuery.Message.Message.Chat.ID)
	chatSession, ok := sessions[update.CallbackQuery.Message.Message.Chat.ID]
	if !ok {
		log.Println("something goes wrong")
		return
	}
	s, ok := chatSession[int64(update.CallbackQuery.Message.Message.ID)]
	if !ok {
		log.Println("something goes wrong")
		return
	}

	superPoke := 0

	isAdmin, isInAdminList := checkAdmins(ctx, b, s.ChatID)[update.CallbackQuery.From.ID]
	if isInAdminList && isAdmin {
		superPoke = 1
		if update.CallbackQuery.Data == "button_downvote" {
			superPoke = -1
		}
	}

	if s.OwnerID == update.CallbackQuery.From.ID && superPoke == 0 {
		log.Println("try to vote to it's own")
		return
	}
	voteResult := 1
	if update.CallbackQuery.Data == "button_downvote" {
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
		result, err := b.BanChatMember(ctx, &bot.BanChatMemberParams{
			ChatID: s.ChatID,
			UserID: s.UserID,
		})
		if err != nil {
			log.Printf("Can't ban user %v %d ", err, s.ChatID)
		}
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

		user, err := getUser(ctx, s.UserID)
		var banUsertag string

		if err == nil {
			if len(user.Username) == 0 {
				banUsertag = fmt.Sprintf("[%s](tg://user?id=%d)", user.AltUsername, user.Uid)
			} else {
				banUsertag = fmt.Sprintf("@%s", escape(user.Username))
			}
		} else {
			banUsertag = fmt.Sprintf("[Пользователь вне базы](tg://user?id=%d)", s.UserID)
		}
		// TODO: unban link
		report := fmt.Sprintf("%s с результатом %v", banUsertag, result)

		userMessages, err := getUserLastNthMessages(ctx, s.UserID, s.ChatID, 20)
		messageIDs := make([]int, len(userMessages))

		if err == nil && len(userMessages) > 0 {

			text := make([]string, len(userMessages))
			for i, v := range userMessages {
				messageIDs[i] = int(v.MessageID)
				text[i] = escape(v.Text)
			}
			escapedText := strings.Join(text, "\n>")
			// TODO: add markdown escape
			report = fmt.Sprintf("%s\nПоследние сообщения от пользователя:\n>%s", report, escapedText)
		}
		// log.Println(report)
		for _, v := range messageIDs {
			_, err = b.DeleteMessage(ctx, &bot.DeleteMessageParams{
				ChatID:    s.ChatID,
				MessageID: int(v),
			})
			if err != nil {
				log.Printf("Can't delete messages for chat %d, user %d. IDs: %v. Err: %v", s.ChatID, s.UserID, v, err)
			}
		}
		// this is works like a shit
		// _, err = b.DeleteMessages(ctx, &bot.DeleteMessagesParams{
		// 	ChatID:     s.ChatID,
		// 	MessageIDs: messageIDs,
		// })
		// if err != nil {
		// 	log.Printf("Can't delete messages for chat %d, user %d. IDs: %v. Err: %v", s.ChatID, s.UserID, messageIDs, err)
		// }
		delete(chatSession, int64(update.CallbackQuery.Message.Message.ID))
		disablePreview := &models.LinkPreviewOptions{IsDisabled: bot.True()}

		// TODO: delete user and user's messages
		_, err = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:             198082233,
			Text:               report,
			ParseMode:          models.ParseModeMarkdown,
			ReplyMarkup:        getBanMessageKeyboard(s.ChatID, s.UserID),
			LinkPreviewOptions: disablePreview,
		})
		if err != nil {
			log.Printf("Can't send report %v", err)
		}
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
		ReplyMarkup: getVoteButtons(upvoteCount, downvoteCount),
	})

}

func handler_poke(ctx context.Context, b *bot.Bot, update *models.Update) {

	// PAUSE
	chatId := update.Message.Chat.ID
	chatSettings := getChatSettings(ctx, chatId)
	if chatSettings.Pause {
		onPauseMessage(ctx, b, update.Message)
		return
	}

	pokeConter := 0
	result := linkRegex.FindAllStringSubmatch(update.Message.Text, -1)

	for i := range result {
		if result[i][1] == "" {
			linkUsername := result[i][2]
			if linkUsername != update.Message.Chat.Username {
				log.Printf("Chat Username is not match %s != %s\n", linkUsername, update.Message.Chat.Username)
				continue
			}
		} else {
			parsedID, _ := strconv.ParseInt("-100"+result[i][2], 10, 64)
			if chatId != parsedID {
				log.Printf("Chat ID is not match %d != %d\n", parsedID, chatId)
				continue
			}
		}
		pokeMessageID, err := strconv.ParseInt(result[i][3], 10, 64)
		if err != nil {
			log.Printf("Message ID is curropted %s", result[i][3])
			continue
		}

		banInfo, err := getBanInfo(ctx, chatId, pokeMessageID)
		banInfo.OwnerID = update.Message.From.ID
		banInfo.RequestMessageID = int64(update.Message.ID)
		if err != nil {
			systemAnswerToMessage(ctx, b, chatId, update.Message.ID, "Извените сообщение не найдено, исользуйте альтернативный метод через \"/ban @username\"")
			continue
		}
		if checkForDuplicates(ctx, chatId, banInfo.UserID, b, update) {
			continue
		}
		if !makeVoteMessage(ctx, banInfo, b) {
			continue
		}
		// if not false
		pokeConter = pokeConter + 1
	}
	// save for statistic
	userMakeVote(ctx, update.Message.From.ID, pokeConter)
}

func checkAdmins(ctx context.Context, b *bot.Bot, chatID int64) (chatAdmins map[int64]bool) {
	chatAdmins, rep := admins[chatID]
	if !rep {
		chatAdmins = getAdmins(ctx, b, chatID)
		admins[chatID] = chatAdmins
	}
	return chatAdmins
}

func pauseHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	// CHECK
	chatAdmins := checkAdmins(ctx, b, update.Message.Chat.ID)
	_, rep := chatAdmins[update.Message.From.ID]

	b.DeleteMessage(ctx, &bot.DeleteMessageParams{
		ChatID:    update.Message.Chat.ID,
		MessageID: update.Message.ID,
	})
	if !rep {
		return
	}
	log.Printf("Pause handler: %s", update.Message.Text)
	chatSettings, rep := settings[update.Message.Chat.ID]
	if !rep {
		// make func
		chatSettings = &DyncmicSetting{
			ChatID: update.Message.Chat.ID,
			Pause:  false,
		}
	}
	var message string
	if strings.Contains(update.Message.Text, "enable") {
		chatSettings.Pause = true
		message = "Пауза активированна"
	} else {
		chatSettings.Pause = false
		message = "Пауза выключенна"
	}
	writeChatSettings(ctx, update.Message.Chat.ID, chatSettings)

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
	chatSettings := getChatSettings(ctx, chatId)
	if chatSettings.Pause {
		onPauseMessage(ctx, b, update.Message)
		return
	}

	for _, v := range update.Message.Entities {
		log.Printf("Message type: %v, entities %s", v.Type, update.Message.Text[v.Offset:v.Offset+v.Length])
		var err error
		var banInfo *BanInfo
		pokeMessageID := int64(0)
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
			banInfo, err = getBanInfoByUsername(ctx, chatId, username)
			if err != nil {
				log.Printf("TODO: return error to user! %v", err)
				continue
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
				pokeMessageID, err = strconv.ParseInt(rxResult[i][3], 10, 64)
				if err != nil {
					log.Printf("Message ID is curropted %s", rxResult[i][3])
					continue
				}
				banInfo, err = getBanInfo(ctx, chatId, pokeMessageID)
				if err != nil {
					systemAnswerToMessage(ctx, b, chatId, update.Message.ID, "Извените сообщение не найдено, исользуйте альтернативный метод через \"/ban @username\"")
					continue
				}
				banInfo.TargetMessageID = pokeMessageID
			}
		}
		banInfo.OwnerID = update.Message.From.ID
		banInfo.RequestMessageID = int64(update.Message.ID)
		if banInfo == nil {
			continue
		}
		if checkForDuplicates(ctx, chatId, banInfo.UserID, b, update) {
			continue
		}
		log.Printf("Start vote process score for user %d %d", banInfo.UserID, banInfo.Score)
		if !makeVoteMessage(ctx, banInfo, b) {
			continue
		}
		pokeConter = pokeConter + 1

	}
	userMakeVote(ctx, update.Message.From.ID, pokeConter)

}

func actionCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
		ShowAlert:       false,
	})
	log.Println(len(update.CallbackQuery.Data))
	data, err := unmarshal(update.CallbackQuery.Data[2:])
	if err != nil {
		log.Printf("Action is parsed bad %v", err)
		return
	}

	chatAdmins := checkAdmins(ctx, b, data.ChatID)
	_, rep := chatAdmins[update.CallbackQuery.From.ID]

	if !rep {
		log.Printf("User %d try to use admin prev for chat %d", update.CallbackQuery.From.ID, data.ChatID)
		return
	}

	switch data.Action {
	case ACTION_UNBAN:
		{
			userIdRaw, ok := data.Data[DATA_TYPE_USERID]
			if !ok {
				log.Printf("TODO replace to message to user: err there are no userid")
				return
			}
			ok, err := unbanUser(ctx, b, data.ChatID, getInt(userIdRaw))
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
				Text:   "Пользователь разбанен",
				ReplyParameters: &models.ReplyParameters{
					ChatID:    update.CallbackQuery.From.ID,
					MessageID: update.CallbackQuery.Message.Message.ID,
				},
			})
		}
	case ACTION_DELETE_ALL:
		{
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
	}
}

func testHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatId := update.Message.Chat.ID
	publicInt, _ := strconv.ParseInt(makePublicGroupString(chatId), 10, 64)

	testData := &Item{
		Action: 1,
		ChatID: publicInt,
		Data:   map[uint8]interface{}{1: 23, 4: 4342},

		// Data: "test",
	}

	enc, err := marshal(testData)
	if err != nil {
		log.Printf("Shit happens %v", err)
		return
	}

	log.Printf("Button data %s, length %d\n", enc, len(enc))

	_, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatId,
		Text:      "Test test",
		ParseMode: models.ParseModeMarkdown,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{
					{Text: "testbtn", CallbackData: fmt.Sprintf("b_%s", enc)},
				},
			},
		},
	})
	if err != nil {
		log.Println(err)
	}
}
