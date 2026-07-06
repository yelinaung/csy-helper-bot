// csy-helper-bot is a Telegram bot that provides stock analysis, code
// explanation, and other developer helper utilities.
package main

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	appbot "gitlab.com/yelinaung/csy-helper-bot/internal/bot"
	appotel "gitlab.com/yelinaung/csy-helper-bot/internal/otel"
)

var (
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	// Load .env before reading any configuration so that OTEL_* and LOG_LEVEL
	// settings sourced from .env are visible to telemetry setup. godotenv does
	// not override vars already present in the real environment.
	_ = godotenv.Load()

	level, err := zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))))
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	zerolog.TimeFieldFormat = time.RFC3339
	zerolog.ErrorMarshalFunc = appotel.SanitizeErrorValue //nolint:reassign // Redact credential-bearing URLs from all zerolog Err fields.

	ctx := context.Background()

	otelShutdown, otelLogWriter, otelErr := appotel.Setup(ctx, appotel.BuildInfo{
		Commit: commit,
		Date:   buildDate,
	})
	if otelErr != nil {
		log.Warn().Err(otelErr).Msg("OpenTelemetry setup failed; continuing without telemetry")
	}

	log.Logger = zerolog.New(io.MultiWriter(
		zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: time.RFC3339,
		},
		otelLogWriter,
	)).With().Timestamp().Caller().Logger()
	log.Info().Msgf("Logger initialized (level=%s)", zerolog.GlobalLevel().String())
	log.Info().Str("commit", commit).Str("build_date", buildDate).Msg("Build info")

	// Emit the error through the OTel-backed logger BEFORE flushing telemetry,
	// so startup/runtime failures (missing token, GetMe failure) are exported.
	// zerolog's log.Fatal exits immediately, so we log at Error, flush, then
	// exit with the same status code log.Fatal would use.
	runErr := appbot.Run()
	if runErr != nil {
		log.Error().Err(runErr).Msg("Bot stopped")
		_ = otelShutdown()
		os.Exit(1)
	}
	_ = otelShutdown()
}
