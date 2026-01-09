# CSY Helper Bot

A Telegram bot that provides helpful utilities for developers.

## Features

- `/lc` or `!lc` - Fetches the daily LeetCode question with title, difficulty, and link

## Setup

1. Create a bot via [@BotFather](https://t.me/BotFather) and get your token
2. Create a `.env` file:
   ```
   TELEGRAM_BOT_TOKEN=your_token_here
   ```
3. Run the bot:
   ```bash
   go run .
   ```

## Deployment

### Docker

```bash
docker build -t csy-helper-bot .
docker run -e TELEGRAM_BOT_TOKEN=your_token csy-helper-bot
```

### Dokku

```bash
# On your server
dokku apps:create csy-helper-bot
dokku config:set csy-helper-bot TELEGRAM_BOT_TOKEN=your_token
dokku checks:disable csy-helper-bot web

# On your local machine
git remote add dokku dokku@your-server:csy-helper-bot
git push dokku master
```

## Group Privacy

If using `!lc` in groups, you may need to disable Group Privacy Mode via [@BotFather](https://t.me/BotFather) → Bot Settings → Group Privacy → Turn off.
