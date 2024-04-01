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

type session struct {
	chatID           int64
	voteMessageID    int64
	requestMessageID int
	ownerID          int64
	targetUserID     int64
	voiters          map[int64]int8
	requiredVotes    int16
}

const (
	MESSAGE_TTL_DAYS = 14

	DEFAULT_SCORE int16 = 3
	LOW_SCORE     int16 = 3
	MID_SCORE     int16 = 5
	HIGH_SCORE    int16 = 10
)

var (
	myBot *bot.Bot

	myID      string
	mainRegex *regexp.Regexp
	linkRegex *regexp.Regexp = regexp.MustCompile(`(?:\s*https://t\.me/(c/)?([\d\w]+)/(\d+))`)

	sessions map[int64]map[int64]session = make(map[int64]map[int64]session)

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
			log.Printf("%s say: %s, lang code %s", update.Message.From.FirstName, update.Message.Text, update.Message.From.LanguageCode)
			userPlusOneMessage(ctx, update.Message.From.ID, update.Message.From.Username)
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
		sessions[chatId] = map[int64]session{}
		chatSessions = sessions[chatId]
	}

	for responceMessage, messageSession := range chatSessions {
		if messageSession.targetUserID != userid {
			continue
		}
		systemAnswerToMessage(ctx, b, chatId, update.Message.ID, fmt.Sprintf("[Уже есть голосовалка](tg://privatepost?channel=%s&post=%d)", makePublicGroupString(chatId), responceMessage))
		return true
	}
	return false
}

func makeVoteMessage(ctx context.Context, chatId int64, userScore *ScoreResult, banMessage string, b *bot.Bot, message *models.Message) bool {
	// move to a separate function
	var requiredScore int16
	if userScore.Rating < 10 {
		requiredScore = LOW_SCORE
	} else if userScore.Rating < 100 {
		requiredScore = MID_SCORE
	} else {
		requiredScore = HIGH_SCORE
	}

	log.Printf("Message: %s", banMessage)

	responceMessage, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatId,
		Text:      fmt.Sprintf("Голосуем за бан %s", banMessage),
		ParseMode: models.ParseModeMarkdown,
		ReplyParameters: &models.ReplyParameters{
			ChatID:    chatId,
			MessageID: message.ID,
		},
		ReplyMarkup: getVoteButtons(0, 0),
	})
	if err != nil {
		log.Printf("Some error %v", err)
		return false
	}
	log.Printf("Responce id %d\n", responceMessage.ID)

	chatSessions, ok := sessions[chatId]
	if !ok {
		sessions[chatId] = map[int64]session{}
		chatSessions = sessions[chatId]
	}
	chatSessions[int64(responceMessage.ID)] = session{
		chatID:           chatId,
		voteMessageID:    int64(responceMessage.ID),
		requestMessageID: message.ID,
		ownerID:          message.From.ID,
		targetUserID:     userScore.Userid,
		voiters:          map[int64]int8{},
		requiredVotes:    requiredScore,
	}
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
	if checkAdmins(ctx, b, s.chatID)[update.CallbackQuery.Message.Message.From.ID] {
		superPoke = 1
		if update.CallbackQuery.Data == "button_downvote" {
			superPoke = -1
		}
	}

	if s.ownerID == update.CallbackQuery.From.ID && superPoke == 0 {
		log.Println("try to vote to it's own")
		return
	}
	voteResult := 1
	if update.CallbackQuery.Data == "button_downvote" {
		voteResult = -1
	}
	s.voiters[update.CallbackQuery.From.ID] = int8(voteResult)

	upvoteCount := 0
	downvoteCount := 0
	for _, v := range s.voiters {
		if v == 1 {
			upvoteCount = upvoteCount + 1
		} else {
			downvoteCount = downvoteCount + 1
		}
	}

	if superPoke == 1 || upvoteCount-downvoteCount >= int(s.requiredVotes) {
		//move to separate function
		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: s.chatID,
			Text:   fmt.Sprintf("Тык! 👉 в юзера %d для чата %d", s.targetUserID, s.chatID),
		})
		if err != nil {
			log.Printf("Can't send message %v%d ", err, s.chatID)
		}
		//Delete the vote message
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    s.chatID,
			MessageID: int(s.voteMessageID),
		})
		// Delete the vote request
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    s.chatID,
			MessageID: s.requestMessageID,
		})

		userMessages, err := getUserLastNthMessages(ctx, s.targetUserID, s.chatID, 20)
		if err == nil && len(userMessages) > 0 {

			text := make([]string, len(userMessages))
			for i, v := range userMessages {
				text[i] = v.Text
			}
			log.Printf("User: %s\n Write messages:\n%s", userMessages[0].UserName, strings.Join(text, "\n"))
		}
		delete(chatSession, int64(update.CallbackQuery.Message.Message.ID))
		// ToDo: prepare report
		// delete user and user's messages
		return
	}
	// Downvoted
	if superPoke == -1 || downvoteCount-upvoteCount >= int(MID_SCORE) {
		//Delete the vote message
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    s.chatID,
			MessageID: int(s.voteMessageID),
		})
		// Delete the vote request
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    s.chatID,
			MessageID: s.requestMessageID,
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

		// get user score
		userScore, err := getRatingFromMessage(ctx, chatId, pokeMessageID)
		if err != nil {
			systemAnswerToMessage(ctx, b, chatId, update.Message.ID, "Извените сообщение не найдено, исользуйте альтернативный метод через \"/ban @username\"")
			continue
		}
		if checkForDuplicates(ctx, chatId, userScore.Userid, b, update) {
			continue
		}
		if !makeVoteMessage(ctx, chatId, userScore, fmt.Sprintf("[сообщение](tg://privatepost?channel=%s&post=%d)", makePublicGroupString(chatId), pokeMessageID), b, update.Message) {
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
		var userScore *ScoreResult
		var err error
		var banMessage string
		if v.Type == models.MessageEntityTypeTextMention {
			log.Printf("Raw data %v ,%v %v", v.Type, v.User.ID, v.User.FirstName)
			userScore, err = getRatingFromUserID(ctx, v.User.ID)
			if err != nil {
				log.Printf("TODO: return error to user! %v", err)
				continue
			}
			banMessage = fmt.Sprintf("[%s %s](tg://user?id=%d)", v.User.FirstName, v.User.LastName, v.User.ID)
		}
		if v.Type == models.MessageEntityTypeMention {
			username := update.Message.Text[v.Offset+1 : v.Offset+v.Length]
			log.Printf("mention username @%s", username)
			userScore, err = getRatingFromUsername(ctx, username)
			if err != nil {
				log.Printf("TODO: return error to user! %v", err)
				continue
			}
			banMessage = fmt.Sprintf("@%s", username)
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
				userScore, err = getRatingFromMessage(ctx, chatId, pokeMessageID)
				if err != nil {
					systemAnswerToMessage(ctx, b, chatId, update.Message.ID, "Извените сообщение не найдено, исользуйте альтернативный метод через \"/ban @username\"")
					continue
				}
				banMessage = fmt.Sprintf("[сообщение](tg://privatepost?channel=%s&post=%d)", makePublicGroupString(chatId), pokeMessageID)
			}
		}
		if userScore == nil {
			continue
		}
		if checkForDuplicates(ctx, chatId, userScore.Userid, b, update) {
			continue
		}
		if !makeVoteMessage(ctx, chatId, userScore, banMessage, b, update.Message) {
			continue
		}
		pokeConter = pokeConter + 1

	}
	userMakeVote(ctx, update.Message.From.ID, pokeConter)

}
