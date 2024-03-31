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
	pokeMessageID    int64
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

func ticker(ctx context.Context, delaySeconds int64, arg func()) {
	ticker := time.NewTicker(time.Duration(delaySeconds * int64(time.Second)))
	for {
		select {
		case <-ctx.Done():
			// fmt.Println("Context kill")
			return
		case <-ticker.C:
			// case t := <-ticker.C:
			// fmt.Println("Tick at", t)
			arg()
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

	b, err := bot.New(botApiKey, opts...)
	if err != nil {
		panic(err)
	}
	me, err := b.GetMe(ctx)
	if err != nil {
		panic(err)
	}
	myID = me.Username

	admins = make(map[int64]map[int64]bool)
	for k := range settings {
		admins[k] = getAdmins(ctx, b, k)
	}

	mainRegex = regexp.MustCompile(fmt.Sprintf(`%s\s*https://t\.me/(c/)?([\d\w]+)/(\d+)`, myID))
	b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, mainRegex, handler_poke)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/pause", bot.MatchTypePrefix, pauseHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/ban", bot.MatchTypePrefix, banHandler)
	log.Printf("Starting %s", me.Username)
	b.Start(ctx)
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
			ReplyParameters: &models.ReplyParameters{
				ChatID:    s.chatID,
				MessageID: int(s.pokeMessageID),
			},
		})
		if err != nil {
			log.Printf("Can't send message %v %d %d ", err, int(s.pokeMessageID), s.chatID)
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
	chatSettings, prs := settings[update.Message.Chat.ID]
	if !prs {
		chatSettings = &DyncmicSetting{
			ChatID: update.Message.Chat.ID,
			Pause:  false,
		}
		writeChatSettings(ctx, update.Message.Chat.ID, chatSettings)
	}

	if chatSettings.Pause {
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    update.Message.Chat.ID,
			MessageID: update.Message.ID,
		})
		replay, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "Бот на паузе",
		})
		if err != nil {
			return
		}
		go dealy(ctx, 30, func() {
			b.DeleteMessage(ctx, &bot.DeleteMessageParams{
				ChatID:    replay.Chat.ID,
				MessageID: replay.ID,
			})
		})
		return
	}

	log.Printf("message %s", update.Message.Text)
	pokeConter := 0

	result := linkRegex.FindAllStringSubmatch(update.Message.Text, -1)
MESSAGE_PARSING_LOOP:
	for i := range result {
		chatId := update.Message.Chat.ID
		if result[i][1] == "" {
			linkUsername := result[i][2]
			if linkUsername != update.Message.Chat.Username {
				log.Printf("Chat Username is not match %s != %s\n", linkUsername, update.Message.Chat.Username)
				continue
			}
		} else {
			parsedID, _ := strconv.ParseInt("-100"+result[i][2], 10, 64)
			if chatId != parsedID {
				log.Printf("Chat ID is not match %d != %d\n", parsedID, update.Message.Chat.ID)
				continue
			}
		}
		pokeMessageID, err := strconv.ParseInt(result[i][3], 10, 64)
		if err != nil {
			log.Printf("Message ID is curropted %s", result[i][3])
			continue
		}
		chatSessions, ok := sessions[chatId]
		if !ok {
			sessions[chatId] = map[int64]session{}
			chatSessions = sessions[chatId]
		}
		// Check for duplicates
		for responceMessage, messageSession := range chatSessions {
			if messageSession.pokeMessageID != pokeMessageID {
				continue
			}
			// log.Printf("$$$ %v %v %v", update.Message.Chat.Type, update.Message.Chat.Username)
			var responceLink string
			// TODO: maybe move to separate func
			if update.Message.Chat.Username != "" {
				responceLink = fmt.Sprintf("https://t.me/%s/%d", update.Message.Chat.Username, responceMessage)
			} else {
				responceLink = fmt.Sprintf("https://t.me/c/%s/%d", makePublicGroupString(chatId), responceMessage)
			}

			reply, err := b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatId,
				Text:   fmt.Sprintf("Уже есть голосовалка %s", responceLink),
				ReplyParameters: &models.ReplyParameters{
					ChatID:    chatId,
					MessageID: update.Message.ID,
				},
			})
			if err != nil {
				log.Printf("Can't send message about the duplicate")
			}
			removeChatID := chatId
			removeReplyID := reply.ID
			removeOriginalID := update.Message.ID
			go dealy(ctx, 2*60, func() {
				b.DeleteMessage(ctx, &bot.DeleteMessageParams{
					ChatID:    removeChatID,
					MessageID: removeOriginalID,
				})
				b.DeleteMessage(ctx, &bot.DeleteMessageParams{
					ChatID:    removeChatID,
					MessageID: removeReplyID,
				})
			})
			continue MESSAGE_PARSING_LOOP
		}

		pokeConter = pokeConter + 1

		// get messsage from link
		// get user score
		var requiredScore int16
		userScore, err := getRatingFromMessage(ctx, chatId, pokeMessageID)
		if err != nil {
			// TODO: new message
			log.Printf("Have to show an alterantive ban way")
			continue MESSAGE_PARSING_LOOP
		}

		// move to a separate function
		if userScore.Rating < 10 {
			requiredScore = LOW_SCORE
		} else if userScore.Rating < 100 {
			requiredScore = MID_SCORE
		} else {
			requiredScore = HIGH_SCORE
		}

		responceMessage, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatId,
			Text:   fmt.Sprintf("Голосуем за бан %s", result[i][0]),
			ReplyParameters: &models.ReplyParameters{
				ChatID:    chatId,
				MessageID: update.Message.ID,
			},
			ReplyMarkup: getVoteButtons(0, 0),
		})
		if err != nil {
			log.Printf("Some error %v", err)
			continue
		}
		log.Printf("Responce id %d\n", responceMessage.ID)

		chatSessions[int64(responceMessage.ID)] = session{
			chatID:           chatId,
			voteMessageID:    int64(responceMessage.ID),
			requestMessageID: update.Message.ID,
			ownerID:          update.Message.From.ID,
			targetUserID:     userScore.Userid,
			pokeMessageID:    pokeMessageID,
			voiters:          map[int64]int8{},
			requiredVotes:    requiredScore,
		}
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
	for _, v := range update.Message.Entities {
		log.Printf("Message type: %v, entities %s", v.Type, update.Message.Text[v.Offset:v.Offset+v.Length])
		if v.Type == models.MessageEntityTypeTextMention {
			log.Printf("Raw data %v ,%v", v.Type, v.User.ID)
		}
		if v.Type == models.MessageEntityTypeMention {
			log.Printf("mention username %s", update.Message.Text[v.Offset:v.Offset+v.Length])
			// b.Get
		}
	}

}
