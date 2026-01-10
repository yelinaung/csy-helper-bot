# CSY Helper Bot

A Telegram bot that provides helpful utilities for developers.

## Features

- `/lc` or `!lc` - Fetches the daily LeetCode question with title, difficulty, and link
- `!s SYMBOL` - Get real-time stock price (e.g., `!s AAPL`)

## Setup

1. Create a bot via [@BotFather](https://t.me/BotFather) and get your token
2. Get a free API key from [Finnhub](https://finnhub.io/) for stock prices
3. Create a `.env` file:
   ```
   TELEGRAM_BOT_TOKEN=your_token_here
   FINNHUB_API_KEY=your_finnhub_key_here
   ```
4. Run the bot:
   ```bash
   go run .
   ```

## Deployment

### Docker

```bash
docker build -t csy-helper-bot .
docker run -e TELEGRAM_BOT_TOKEN=your_token -e FINNHUB_API_KEY=your_key csy-helper-bot
```

### Dokku

```bash
# On your server
dokku apps:create csy-helper-bot
dokku config:set csy-helper-bot TELEGRAM_BOT_TOKEN=your_token
dokku config:set csy-helper-bot FINNHUB_API_KEY=your_key

# On your local machine
git remote add dokku dokku@your-server:csy-helper-bot
git push dokku master
```

## Group Privacy

If using `!lc` or `!s` in groups, you may need to disable Group Privacy Mode via [@BotFather](https://t.me/BotFather) → Bot Settings → Group Privacy → Turn off.

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
