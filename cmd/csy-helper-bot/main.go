package main

import (
	"log"

	appbot "gitlab.com/yelinaung/csy-helper-bot/internal/bot"
)

func main() {
	if err := appbot.Run(); err != nil {
		log.Fatal(err)
	}
}
