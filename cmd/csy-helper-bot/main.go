package main

import (
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	appbot "gitlab.com/yelinaung/csy-helper-bot/internal/bot"
)

var (
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	level, err := zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))))
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.RFC3339,
	}).With().Timestamp().Caller().Logger()
	log.Info().Msgf("Logger initialized (level=%s)", zerolog.GlobalLevel().String())
	log.Info().Str("commit", commit).Str("build_date", buildDate).Msg("Build info")

	if err := appbot.Run(); err != nil {
		log.Fatal().Err(err).Msg("Bot stopped")
	}
}
