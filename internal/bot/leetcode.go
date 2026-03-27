package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/rs/zerolog/log"
)

type LeetCodeQuestion struct {
	Title      string
	TitleSlug  string
	Difficulty string
}

type graphQLRequest struct {
	Query string `json:"query"`
}

type graphQLResponse struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Data struct {
		ActiveDailyCodingChallengeQuestion struct {
			Question struct {
				Title      string `json:"title"`
				TitleSlug  string `json:"titleSlug"` //nolint:tagliatelle // LeetCode GraphQL response uses camelCase.
				Difficulty string `json:"difficulty"`
			} `json:"question"`
		} `json:"activeDailyCodingChallengeQuestion"` //nolint:tagliatelle // LeetCode GraphQL response uses camelCase.
	} `json:"data"`
}

func lcHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	question, err := fetchDailyLeetCode(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to fetch LeetCode daily question")
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            "Failed to fetch LeetCode daily question. Please try again later.",
		})
		return
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            formatLeetCodeMessage(question),
	})
}

func fetchDailyLeetCode(ctx context.Context) (*LeetCodeQuestion, error) {
	query := `{
		activeDailyCodingChallengeQuestion {
			question {
				title
				titleSlug
				difficulty
			}
		}
	}`

	reqBody := graphQLRequest{Query: query}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", leetCodeGraphQLURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	// URL is the trusted leetCodeGraphQLURL constant.
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(unexpectedCodeErrMsg, resp.StatusCode)
	}

	var gqlResp graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return nil, err
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", gqlResp.Errors[0].Message)
	}

	q := gqlResp.Data.ActiveDailyCodingChallengeQuestion.Question
	if q.Title == "" || q.TitleSlug == "" || q.Difficulty == "" {
		return nil, errors.New("daily question data missing")
	}

	return &LeetCodeQuestion{
		Title:      q.Title,
		TitleSlug:  q.TitleSlug,
		Difficulty: q.Difficulty,
	}, nil
}

func formatLeetCodeMessage(question *LeetCodeQuestion) string {
	difficultyEmoji := map[string]string{
		"Easy":   "🟩",
		"Medium": "🟨",
		"Hard":   "🟥",
	}

	emoji := difficultyEmoji[question.Difficulty]
date := nowFunc().UTC().Format(dateFormatPattern)
	url := fmt.Sprintf("https://leetcode.com/problems/%s/", question.TitleSlug)

	return fmt.Sprintf("Date: %s\nTitle: %s\nDifficulty: %s %s\n%s",
		date, question.Title, question.Difficulty, emoji, url)
}
