# CSY Helper Bot

[![CI/CD Pipeline](https://github.com/yelinaung/csy-helper-bot/actions/workflows/ci.yml/badge.svg)](https://github.com/yelinaung/csy-helper-bot/actions/workflows/ci.yml)

> [!IMPORTANT]
> **Disclaimer**: AI coding agents (Claude/Amp) wrote most of this bot as an experiment. It runs, but nothing guarantees its quality. Review the code before you deploy it — it may contain bugs or security holes. Use at your own risk.

A Telegram bot that posts stock quotes and charts, answers questions with Gemini, and fetches the daily LeetCode problem for allowlisted group chats.

## Commands

- `/lc` or `!lc` — posts the daily LeetCode question with its title, difficulty, and link
- `!s AAPL` — real-time stock quote
- `!s AAPL 7d` — historical chart image with a summary (`30d`, `60d`, and `90d` also work)
- `!sa AAPL` — stock analysis: the current quote, latest news from Exa, and a Gemini summary
- `@<bot_username> <question>` — answers the question with Gemini, with or without a quoted message (e.g. `@<bot_username> what does mutex mean?`, or reply to a message and ask `can you explain this?`)

When the question or the quoted message contains Burmese, the bot answers in Burmese. Each answer picks a random tone with a matching facial-expression emoji. An in-memory rate limiter caps how often users can ask.

## Setup

1. Create a bot via [@BotFather](https://t.me/BotFather) and copy the token
2. Get a free [Finnhub](https://finnhub.io/) API key for stock quotes
3. Get a [Databento](https://databento.com/) API key for historical stock data
4. Get an [Exa](https://exa.ai/) API key for web search (the `!sa` command requires it)
5. (Optional) Get a [Parallel](https://parallel.ai/) API key so answers about current events draw on fresh web search results
6. Create a `.env` file:
   ```
   TELEGRAM_BOT_TOKEN=your_token_here
   FINNHUB_API_KEY=your_finnhub_key_here
   DATABENTO_API_KEY=your_databento_key_here
   # optional (defaults to EQUS.MINI)
   DATABENTO_DATASET=EQUS.MINI
   GEMINI_API_KEY=your_gemini_key_here
   # optional (defaults to gemini-3.5-flash)
   GEMINI_MODEL=gemini-3.5-flash
   # optional (defaults to 60)
   GEMINI_TIMEOUT_SECONDS=60
   # Stock analysis (optional — requires GEMINI_API_KEY + EXA_API_KEY)
   STOCK_ANALYSIS_ENABLED=true
   EXA_API_KEY=your_exa_key_here
   # optional (defaults to GEMINI_MODEL or gemini-3.5-flash)
   STOCK_ANALYSIS_MODEL=gemini-3.5-flash
   # optional (defaults to 90)
   STOCK_ANALYSIS_TIMEOUT_SECONDS=90
   # optional (defaults to 5 requests per 300 seconds)
   STOCK_ANALYSIS_RATE_LIMIT_COUNT=5
   STOCK_ANALYSIS_RATE_LIMIT_WINDOW_SECONDS=300
   # optional (defaults to 5, capped at 20)
   EXA_NUM_RESULTS=5
   # Web search for fresh-info questions (optional — requires GEMINI_API_KEY)
   PARALLEL_API_KEY=your_parallel_key_here
   # optional (defaults to 15)
   PARALLEL_TIMEOUT_SECONDS=15
   # optional (defaults to 5, capped at 10)
   PARALLEL_MAX_RESULTS=5
   ALLOWED_GROUP_IDS=-1001234567890,-1009876543210
   EXPLAIN_RATE_LIMIT_COUNT=5
   EXPLAIN_RATE_LIMIT_WINDOW_SECONDS=60
   LOG_LEVEL=info
   # OpenTelemetry (optional — disabled unless OTEL_ENABLED=true)
   OTEL_ENABLED=true
   # OTLP/HTTP endpoint (defaults to http://localhost:4318)
   OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
   # Optional: auth headers for hosted collectors
   # OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer ...
   # Optional: override the default service name (csy-helper-bot)
   # OTEL_SERVICE_NAME=csy-helper-bot
   # Optional: disable individual signals while keeping others on
   # OTEL_TRACES_ENABLED=false
   # OTEL_METRICS_ENABLED=false
   # OTEL_LOGS_ENABLED=false
   # Optional: dump to stdout instead of OTLP (local debugging)
   # OTEL_EXPORTER=stdout
   ```
7. Run the bot:
   ```bash
   go run ./cmd/csy-helper-bot
   ```

## Access Control

The bot responds only in groups and supergroups listed in `ALLOWED_GROUP_IDS`. It ignores private chats, and it leaves any group not on the list.

## Observability (OpenTelemetry)

The bot logs to the console through zerolog. Telemetry export is **off by default**; set `OTEL_ENABLED=true` to ship traces, metrics, and logs over OTLP/HTTP to a local collector such as [HyperDX](https://www.hyperdx.io/) or [Clickstack](https://clickstack.io/), both of which ingest on the standard `http://localhost:4318` endpoint.

When enabled, the bot exports:

- **Traces** — one span per registered handler plus child spans for every
  external call (Finnhub, Databento, LeetCode, Exa, Parallel, Telegram photo
  download, Gemini). HTTP client spans and metrics come from `otelhttp`.
- **Metrics** — `bot.commands.total` and `bot.command.duration` (with a
  `bot.result` dimension of `success`/`error`/`rate_limited`/`unknown`/...),
  `bot.rate_limited.total`, and `gen_ai.client.token.usage` (a histogram).
- **Logs** — the zerolog output, bridged into the OTel logs pipeline
  alongside the console output.

### Credential safety

Finnhub puts its API key in the query string and Telegram puts the bot token
in the photo-download URL path. A sanitizing span exporter strips these
credentials from `url.full` / `http.url` before any trace leaves the process
(redacting `token`/`api_key`/`apikey`/`key` query params and `bot<TOKEN>`
path segments, fail-closed on unparseable URLs). The log bridge applies the
same redaction to `url`-bearing log attributes.

### Local debugging

Set `OTEL_EXPORTER=stdout` to print telemetry to stdout instead of exporting
it — no collector needed. Turn off individual signals with
`OTEL_TRACES_ENABLED=false`, `OTEL_METRICS_ENABLED=false`, or
`OTEL_LOGS_ENABLED=false` while keeping the others on.

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
  -e GEMINI_MODEL=gemini-3.5-flash \
  -e GEMINI_TIMEOUT_SECONDS=60 \
  -e STOCK_ANALYSIS_ENABLED=true \
  -e EXA_API_KEY=your_exa_key \
  -e STOCK_ANALYSIS_MODEL=gemini-3.5-flash \
  -e ALLOWED_GROUP_IDS=-1001234567890 \
  -e LOG_LEVEL=info \
  -e OTEL_ENABLED=true \
  -e OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
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
dokku config:set csy-helper-bot GEMINI_MODEL=gemini-3.5-flash
dokku config:set csy-helper-bot GEMINI_TIMEOUT_SECONDS=60
dokku config:set csy-helper-bot STOCK_ANALYSIS_ENABLED=true
dokku config:set csy-helper-bot EXA_API_KEY=your_exa_key
dokku config:set csy-helper-bot STOCK_ANALYSIS_MODEL=gemini-3.5-flash
dokku config:set csy-helper-bot ALLOWED_GROUP_IDS=-1001234567890
dokku config:set csy-helper-bot EXPLAIN_RATE_LIMIT_COUNT=5
dokku config:set csy-helper-bot EXPLAIN_RATE_LIMIT_WINDOW_SECONDS=60
dokku config:set csy-helper-bot LOG_LEVEL=info
# Optional: enable OpenTelemetry export
dokku config:set csy-helper-bot OTEL_ENABLED=true
dokku config:set csy-helper-bot OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318

# On your local machine
git remote add dokku dokku@your-server:csy-helper-bot
git push dokku master
```

## Group Privacy

If the bot misses mentions in a group, disable Group Privacy Mode via [@BotFather](https://t.me/BotFather) → Bot Settings → Group Privacy → Turn off.

## Troubleshooting

### "Conflict: terminated by other getUpdates request"

Telegram allows only one long-polling connection per bot token. This error means a second instance is polling with the same token.

**Solutions:**
1. Stop any local bot instances before deploying
2. Ensure only one container is running on Dokku:
   ```bash
   dokku ps:scale csy-helper-bot web=1
   ```
3. If the error persists during deploys, disable zero-downtime checks:
   ```bash
   dokku checks:disable csy-helper-bot web
   ```
