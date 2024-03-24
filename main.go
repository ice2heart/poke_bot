package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/joho/godotenv"
)

type session struct {
	chatID        int64
	messageID     int64
	ownerID       int64
	targetUserID  int64
	pokeMessageID int64
	voiters       map[int64]int8
	requiredVotes int16
	// date for timer
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
)

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

	// TODO: move to env
	initDb(ctx, mongoAddr, dbName)

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
	mainRegex = regexp.MustCompile(fmt.Sprintf(`%s\s*https://t\.me/(c/)?([\d\w]+)/(\d+)`, myID))
	b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, mainRegex, handler_poke)
	log.Printf("Starting %s", me.Username)
	b.Start(ctx)
}

func logMessagesMiddleware(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message != nil {
			log.Printf("%s say: %s, lang code %s", update.Message.From.FirstName, update.Message.Text, update.Message.From.LanguageCode)
			userPlusOneMessage(ctx, update.Message.From.ID)
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
	if s.ownerID == update.CallbackQuery.From.ID {
		log.Println("try to vote to it's own")
		// return
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
	// TODO: uncomment test code
	// if upvoteCount-downvoteCount >= int(s.requiredVotes) {
	if upvoteCount-downvoteCount > 0 {
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
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    s.chatID,
			MessageID: int(s.messageID),
		})
		delete(chatSession, int64(update.CallbackQuery.Message.Message.ID))
		// ToDo: prepare report
		// delete user and user's messages

	}
	kb := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: fmt.Sprintf("+ (%d)", upvoteCount), CallbackData: "button_upvote"},
				{Text: fmt.Sprintf("- (%d)", downvoteCount), CallbackData: "button_downvote"},
			},
		},
	}
	b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:      update.CallbackQuery.Message.Message.Chat.ID,
		MessageID:   update.CallbackQuery.Message.Message.ID,
		ReplyMarkup: kb,
	})

}

func handler_poke(ctx context.Context, b *bot.Bot, update *models.Update) {
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

			_, err := b.SendMessage(ctx, &bot.SendMessageParams{
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
			continue MESSAGE_PARSING_LOOP
		}

		pokeConter = pokeConter + 1

		kb := &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{
					{Text: "+ (0)", CallbackData: "button_upvote"},
					{Text: "- (0)", CallbackData: "button_downvote"},
				},
			},
		}

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
			ReplyMarkup: kb,
		})
		if err != nil {
			log.Printf("Some error %v", err)
			continue
		}
		log.Printf("Responce id %d\n", responceMessage.ID)

		chatSessions[int64(responceMessage.ID)] = session{
			chatID:        chatId,
			messageID:     int64(responceMessage.ID),
			ownerID:       update.Message.From.ID,
			targetUserID:  userScore.Userid,
			pokeMessageID: pokeMessageID,
			voiters:       map[int64]int8{},
			requiredVotes: requiredScore,
		}
	}
	// save for statistic
	userMakeVote(ctx, update.Message.From.ID, pokeConter)
}

func handler(ctx context.Context, b *bot.Bot, update *models.Update) {

}
