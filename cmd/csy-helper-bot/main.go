package main

import (
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	appbot "gitlab.com/yelinaung/csy-helper-bot/internal/bot"
)

func main() {
	level, err := zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))))
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	log.Info().Str("level", zerolog.GlobalLevel().String()).Msg("Logger initialized")

	if err := appbot.Run(); err != nil {
		log.Fatal().Err(err).Msg("Bot stopped")
	}
}
