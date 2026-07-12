# poke_bot

A Telegram moderation bot for group chats that lets the community vote to ban, mute, or restrict members instead of relying solely on admin action. Written in Go, backed by MongoDB, using both the Bot API and MTProto (for access to chat info the Bot API can't provide).

## Features

- **Community voting** — any member can start a vote to ban, mute, or restrict a user to text-only messages. Votes are decided by a score threshold that scales with the target's reputation (new/low-rep users need fewer votes against them).
- **Reputation/gamification** — users earn points from reactions on their messages; `/best` and `/likes` show leaderboards, `/check` shows a user's score.
- **Spam/flood detection** — a background detector watches reactions and message patterns to flag suspicious activity.
- **Media restriction mode** — `/text_only` votes restrict a user to text-only posting (no stickers/images).
- **Admin panel** — inline buttons on vote messages let admins unban/undo actions directly.
- **Per-chat settings** — pause the bot, set a log channel, set a custom vote tag, stored per chat in MongoDB.
- **Message logging** — optional forwarding of chat activity to a log channel for auditing.

## Commands

| Command | Description |
|---|---|
| `/ban`, `/voteban`, `/voteblan` | Start a vote to ban the replied-to user |
| `/mute`, `/voteeblan` | Start a vote to mute the replied-to user |
| `/text_only` | Start a vote to restrict the replied-to user to text-only messages |
| `/pause` | Pause/unpause bot moderation in the chat (admin only) |
| `/set_channel` | Set the log channel for the chat (admin only) |
| `/set_tag` | Set a custom vote tag/label for the chat |
| `/likes` | Show a user's received reactions |
| `/best` | Show the chat's top-rated members |
| `/check` | Check a user's current score/status |
| `/delete` | Delete a message (admin only) |
| `/start` | Bot greeting/info |
| `/test` | Debug/test command |

## Requirements

- Go 1.25+
- MongoDB
- A Telegram bot token ([@BotFather](https://t.me/BotFather))
- A Telegram API ID/hash for MTProto ([my.telegram.org](https://my.telegram.org/apps))

## Configuration

The bot is configured entirely via environment variables (a `.env` file is loaded automatically):

| Variable | Required | Description |
|---|---|---|
| `BOT_API_KEY` | yes | Telegram bot token |
| `ADMIN_ID` | yes | Telegram user ID of the super admin |
| `BOT_APP_ID` | yes | Telegram API ID (MTProto, from my.telegram.org) |
| `BOT_APP_HASH` | yes | Telegram API hash (MTProto) |
| `MONGO_ADDRES` | no | MongoDB connection string (default `mongodb://localhost:27017`) |
| `MONGO_DB_NAME` | no | MongoDB database name (default `pokebot`) |

## Running

### Docker Compose (recommended)

```bash
cp .env.example .env   # create and fill in your values
docker compose up -d
```

This starts the bot alongside a MongoDB instance, using the prebuilt image from `ghcr.io/ice2heart/poke_bot`.

### Locally

```bash
go run .
```

### Build

```bash
go build -o poke_bot .
```

Releases are built and published via GoReleaser/GitHub Actions on tag push (see `.github/workflows/release.yml`, `.goreleaser.yaml`).

## Testing

```bash
go test ./...
```

## Project layout

- `main.go` — startup, env/config, handler registration, background tickers
- `votes.go`, `vote_handler.go`, `ban_info.go`, `mute_info.go`, `media_restriction.go` — voting mechanics for ban/mute/text-only
- `admin_panel.go` — inline admin action callbacks
- `gamification.go` — reputation, leaderboards
- `detector.go` — reaction/spam detection
- `chat_settings.go`, `db.go` — per-chat settings and MongoDB persistence
- `message_log.go` — activity logging to a channel
- `tg_helpers.go`, `utils.go`, `marshaling.go` — Telegram helpers, callback data (de)serialization
- `mtproto/` — MTProto client wrapper for data the Bot API can't provide
- `cache/` — generic in-memory TTL cache used by the detector
