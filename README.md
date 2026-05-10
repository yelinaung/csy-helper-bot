# CSY Helper Bot

[![CI/CD Pipeline](https://github.com/yelinaung/csy-helper-bot/actions/workflows/ci.yml/badge.svg)](https://github.com/yelinaung/csy-helper-bot/actions/workflows/ci.yml)

> [!IMPORTANT]
> **Disclaimer**: This application was developed primarily by AI coding agents (Claude/Amp) as an experimental project. While functional, **quality is not guaranteed**. If you choose to use or deploy this bot, please do so with c
aution, review the code yourself, and understand that it may contain bugs or security issues. Use at your own risk.

A Telegram bot that provides helpful utilities for developers.

## Features

- `/lc` or `!lc` - Fetches the daily LeetCode question with title, difficulty, and link
- `!s SYMBOL` - Get real-time stock price (e.g., `!s AAPL`)
- `!s SYMBOL 7d|30d|60d|90d` - Get historical stock chart image + summary (e.g., `!s AAPL 7d`)
- `!sa SYMBOL` - AI-generated stock analysis using Exa news + Gemini (e.g., `!sa AAPL`)
- `@<bot_username> <question>` - Asks anything with Gemini (works with or without quoting a message)
- Burmese-aware answers:
  - If requester text or quoted text contains Burmese, answer in Burmese
- Tone/persona variation:
  - Random tone per explain/ask response with matching facial-expression emoji
- In-memory rate limiting for explain/ask requests
- Group allowlist enforcement:
  - Bot is active only in allowlisted groups/supergroups
  - Bot ignores private chats
  - Bot leaves unauthorized groups automatically
- Structured logging with human-readable console output (zerolog)

## Setup

1. Create a bot via [@BotFather](https://t.me/BotFather) and get your token
2. Get a free API key from [Finnhub](https://finnhub.io/) for stock prices
3. Get a Databento API key from [Databento](https://databento.com/) for historical stock data
4. Get an API key from [Exa](https://exa.ai/) for web search (required for `!sa` command)
5. Create a `.env` file:
   ```
   TELEGRAM_BOT_TOKEN=your_token_here
   FINNHUB_API_KEY=your_finnhub_key_here
   DATABENTO_API_KEY=your_databento_key_here
   # optional (defaults to EQUS.MINI)
   DATABENTO_DATASET=EQUS.MINI
   GEMINI_API_KEY=your_gemini_key_here
   # optional (defaults to gemini-2.5-flash)
   GEMINI_MODEL=gemini-3-flash-preview
   # optional (defaults to 60)
   GEMINI_TIMEOUT_SECONDS=60
   # Stock analysis (optional — requires GEMINI_API_KEY + EXA_API_KEY)
   STOCK_ANALYSIS_ENABLED=true
   EXA_API_KEY=your_exa_key_here
   # optional (defaults to gemini-2.5-flash)
   STOCK_ANALYSIS_MODEL=gemini-2.5-flash
   # optional (defaults to 90)
   STOCK_ANALYSIS_TIMEOUT_SECONDS=90
   # optional (defaults to 5 requests per 300 seconds)
   STOCK_ANALYSIS_RATE_LIMIT_COUNT=5
   STOCK_ANALYSIS_RATE_LIMIT_WINDOW_SECONDS=300
   # optional (defaults to 5, capped at 20)
   EXA_NUM_RESULTS=5
   ALLOWED_GROUP_IDS=-1001234567890,-1009876543210
   EXPLAIN_RATE_LIMIT_COUNT=5
   EXPLAIN_RATE_LIMIT_WINDOW_SECONDS=60
   LOG_LEVEL=info
   ```
5. Run the bot:
   ```bash
   go run ./cmd/csy-helper-bot
   ```

## Usage

- Stock commands:
  - `!s AAPL` - current quote
  - `!s AAPL 7d` - 7-day historical chart image
  - `!s AAPL 30d` - 30-day historical chart image (+ 60d/90d also supported)
  - `!sa AAPL` - AI stock analysis (quote + latest news + Gemini summary)
- Ask directly:
  - `@<bot_username> what does mutex mean?`
  - `@<bot_username> can you explain this and that?` (while replying to a quoted message)

## Access Control

- The bot only responds in `group` / `supergroup` chats.
- Private chats are ignored.
- Groups must be listed in `ALLOWED_GROUP_IDS`.
- If a group is not allowlisted, the bot leaves that group.

## Deployment

### Docker

```bash
docker build -t csy-helper-bot .
docker run \
  -e TELEGRAM_BOT_TOKEN=your_token \
  -e FINNHUB_API_KEY=your_key \
  -e DATABENTO_API_KEY=your_databento_key \
  -e DATABENTO_DATASET=EQUS.MINI \
  -e GEMINI_API_KEY=your_gemini_key \
  -e GEMINI_MODEL=gemini-3-flash-preview \
  -e GEMINI_TIMEOUT_SECONDS=60 \
  -e STOCK_ANALYSIS_ENABLED=true \
  -e EXA_API_KEY=your_exa_key \
  -e STOCK_ANALYSIS_MODEL=gemini-2.5-flash \
  -e ALLOWED_GROUP_IDS=-1001234567890 \
  -e LOG_LEVEL=info \
  csy-helper-bot
```

### Dokku

```bash
# On your server
dokku apps:create csy-helper-bot
dokku config:set csy-helper-bot TELEGRAM_BOT_TOKEN=your_token
dokku config:set csy-helper-bot FINNHUB_API_KEY=your_key
dokku config:set csy-helper-bot DATABENTO_API_KEY=your_databento_key
dokku config:set csy-helper-bot DATABENTO_DATASET=EQUS.MINI
dokku config:set csy-helper-bot GEMINI_API_KEY=your_gemini_key
dokku config:set csy-helper-bot GEMINI_MODEL=gemini-3-flash-preview
dokku config:set csy-helper-bot GEMINI_TIMEOUT_SECONDS=60
dokku config:set csy-helper-bot STOCK_ANALYSIS_ENABLED=true
dokku config:set csy-helper-bot EXA_API_KEY=your_exa_key
dokku config:set csy-helper-bot STOCK_ANALYSIS_MODEL=gemini-2.5-flash
dokku config:set csy-helper-bot ALLOWED_GROUP_IDS=-1001234567890
dokku config:set csy-helper-bot EXPLAIN_RATE_LIMIT_COUNT=5
dokku config:set csy-helper-bot EXPLAIN_RATE_LIMIT_WINDOW_SECONDS=60
dokku config:set csy-helper-bot LOG_LEVEL=info

# On your local machine
git remote add dokku dokku@your-server:csy-helper-bot
git push dokku master
```

## Group Privacy

If using commands via mention in groups, you may need to disable Group Privacy Mode via [@BotFather](https://t.me/BotFather) → Bot Settings → Group Privacy → Turn off.

## Troubleshooting

### "Conflict: terminated by other getUpdates request"

This error means multiple bot instances are trying to poll Telegram with the same token. Only one instance can use long-polling at a time.

**Solutions:**
1. Stop any local bot instances before deploying
2. Ensure only one container is running on Dokku:
   ```bash
   dokku ps:scale csy-helper-bot web=1
   ```
3. If issues persist during deploys, disable zero-downtime:
   ```bash
   dokku checks:disable csy-helper-bot web
   ```
