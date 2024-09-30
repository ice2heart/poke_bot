package mtproto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/gotd/contrib/bg"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

type MTProtoHelper struct {
	AppId     int
	AppHash   string
	BotApiKey string
	Logger    *zap.Logger

	tg_client *telegram.Client
	api       *tg.Client
	stop_fn   bg.StopFunc
}

type MTprotoUser struct {
	UserId     int64
	Username   string
	AccessHash int64
}

type Id_Type uint8

const (
	USER Id_Type = iota
	CHANNEL
	CHAT
)

const (
	MIN_CHANNEL_ID = -1002147483647
	MAX_CHANNEL_ID = -1000000000000
	MIN_CHAT_ID    = -2147483647
)

func (client *MTProtoHelper) Init(ctx context.Context) error {
	var err error
	client.tg_client = telegram.NewClient(int(client.AppId), client.AppHash, telegram.Options{Logger: client.Logger})
	client.stop_fn, err = bg.Connect(client.tg_client)
	if err != nil {
		return err
	}
	// // not sure why
	// defer func() { _ = stop() }()

	// Now you can use client.
	if _, err := client.tg_client.Auth().Bot(ctx, client.BotApiKey); err != nil {
		log.Panicf("Can't auth MTProto client: %v", err)
	}
	client.api = client.tg_client.API()
	state, err := client.api.UpdatesGetState(ctx)
	if err != nil {
		return err
	}
	log.Printf("State: %v", state)
	return nil

}

func (client *MTProtoHelper) Stop() {
	client.stop_fn()
}

// def get_peer_id(peer: raw.base.Peer) -> int:
//     """Get the non-raw peer id from a Peer object"""
//     if isinstance(peer, raw.types.PeerUser):
//         return peer.user_id

//     if isinstance(peer, raw.types.PeerChat):
//         return -peer.chat_id

//     if isinstance(peer, raw.types.PeerChannel):
//         return MAX_CHANNEL_ID - peer.channel_id

//     raise ValueError(f"Peer type invalid: {peer}")

// def get_peer_type(peer_id: int) -> str:
//     if peer_id < 0:
//         if MIN_CHAT_ID <= peer_id:
//             return "chat"

//         if MIN_CHANNEL_ID <= peer_id < MAX_CHANNEL_ID:
//             return "channel"
//     elif 0 < peer_id <= MAX_USER_ID:
//         return "user"

//     raise ValueError(f"Peer id invalid: {peer_id}")

func idType(peerId int64) Id_Type {
	if peerId < 0 {
		if MIN_CHAT_ID <= peerId {
			return CHAT
		}
		if MIN_CHANNEL_ID <= peerId && peerId < MAX_CHANNEL_ID {
			return CHANNEL
		}
		return CHANNEL
	}
	return USER
}

func (client *MTProtoHelper) GetAccessHash(ctx context.Context, peerId int64) (hashCode int64, err error) {

	peerType := idType(peerId)
	log.Printf("GetAccessHash peer id %v real id %v, peer type %v", peerId, -1000000000000-peerId, peerType)
	switch peerType {
	case CHANNEL:
		{
			channelsInfo, err := client.api.ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{ChannelID: -1000000000000 - peerId, AccessHash: 0}})
			if err != nil {
				return 0, err
			}
			chats := channelsInfo.GetChats()
			log.Printf("Chats size %v", len(chats))
			for _, chatInfo := range chats {

				switch v := chatInfo.(type) {
				case *tg.Channel:
					return v.AccessHash, nil
				case *tg.ChannelForbidden:

				default:
					return 0, fmt.Errorf("unknown chat type received: %T (expected Channel), %v", v, chatInfo)
				}
			}
		}
	case CHAT:
		{
			chatsInfo, err := client.api.MessagesGetChats(ctx, []int64{-peerId})
			if err != nil {
				return 0, err
			}
			chats := chatsInfo.GetChats()
			for _, chatsInfo := range chats {
				switch v := chatsInfo.(type) {
				case *tg.Chat:
					jcart, _ := json.MarshalIndent(v, "", "\t")
					fmt.Println(string(jcart))
					return 0, nil
				}
			}
			return 0, fmt.Errorf("chat does'n have acess code")

		}
	case USER:
		{
			usersInfo, err := client.api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: peerId}})
			if err != nil {
				return 0, err
			}
			for _, users := range usersInfo {
				switch v := users.(type) {
				case *tg.User:
					return v.AccessHash, nil
				default:
					return 0, fmt.Errorf("unknow type of user %v", v.String())
				}
			}
			return 0, fmt.Errorf("can't find the user access hash")
		}
	default:
		{
			return 0, fmt.Errorf("wrong type of peer %v", peerType)
		}
	}
	return 0, errors.New("func GetAccessHash reach the unreachable return")
}

func (client *MTProtoHelper) GetUser(ctx context.Context, uid int64) (user *MTprotoUser, err error) {

	userInfo, err := client.tg_client.API().UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: uid}})
	if err != nil {
		return nil, fmt.Errorf("can't get user %v", err)
	}
	for _, userInfoItem := range userInfo {
		log.Printf("info typeID %v", userInfoItem.TypeID())
		switch v := userInfoItem.(type) {
		case *tg.User:
			jcart, _ := json.MarshalIndent(v, "", "\t")
			fmt.Println(string(jcart))
			user = &MTprotoUser{
				UserId:     v.ID,
				Username:   v.Username,
				AccessHash: v.AccessHash,
			}
			return user, nil
		default:
			log.Printf("Unknows type %v", userInfoItem.TypeID())

		}
	}
	return nil, errors.New("the user is not found")
}

func (client *MTProtoHelper) GetPeerByUsername(ctx context.Context, username string) (peer tg.InputPeerClass, err error) {

	// var peer tg.InputPeerClass
	userInfo, err := client.api.ContactsResolveUsername(ctx, username)
	if err != nil {
		log.Printf("ContactsResolveUsername faild %v", err)
		return nil, err
	}
	users := userInfo.MapUsers().UserToMap()
	chats := userInfo.MapChats().ChatToMap()
	channels := userInfo.MapChats().ChannelToMap()

	switch p := userInfo.Peer.(type) {
	case *tg.PeerUser: // peerUser#9db1bc6d
		dialog, ok := users[p.UserID]
		if !ok {
			return nil, fmt.Errorf("user %d not found", p.UserID)
		}

		peer = &tg.InputPeerUser{
			UserID:     dialog.ID,
			AccessHash: dialog.AccessHash,
		}
	case *tg.PeerChat: // peerChat#bad0e5bb
		dialog, ok := chats[p.ChatID]
		if !ok {
			return nil, fmt.Errorf("chat %d not found", p.ChatID)
		}

		peer = &tg.InputPeerChat{
			ChatID: dialog.ID,
		}
	case *tg.PeerChannel: // peerChannel#bddde532
		dialog, ok := channels[p.ChannelID]
		if !ok {
			return nil, fmt.Errorf("channel %d not found", p.ChannelID)
		}

		peer = &tg.InputPeerChannel{
			ChannelID:  dialog.ID,
			AccessHash: dialog.AccessHash,
		}
	default:
		return nil, fmt.Errorf("unexpected peer type %T", userInfo)
	}
	return peer, nil

}

func (client *MTProtoHelper) BanUser(ctx context.Context, chatID int64, chatHash int64, username string) (result bool, err error) {

	channel := &tg.InputChannel{ChannelID: -1000000000000 - chatID, AccessHash: chatHash}
	peer, err := client.GetPeerByUsername(ctx, username)

	if err != nil {
		return false, err
	}

	//ban part
	_, err = client.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
		Channel:     channel,
		Participant: peer,
		BannedRights: tg.ChatBannedRights{
			ViewMessages: true,
			SendMessages: true,
			SendMedia:    true,
			SendStickers: true,
			SendGifs:     true,
			SendGames:    true,
			SendInline:   true,
			EmbedLinks:   true,
			SendPolls:    true,
			ChangeInfo:   true,
			InviteUsers:  true,
			PinMessages:  true,
			UntilDate:    0, // forever
		},
	})
	if err != nil {
		return false, err
	}
	return true, nil
}
