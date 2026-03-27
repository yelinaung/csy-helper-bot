package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	dbn "github.com/NimbleMarkets/dbn-go"
	dbn_hist "github.com/NimbleMarkets/dbn-go/hist"
	"github.com/go-analyze/charts"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/rs/zerolog/log"
)

var (
	histHTTPClient = &http.Client{Timeout: 30 * time.Second}
	symbolRegex    = regexp.MustCompile(`^[A-Z0-9.\-]{1,10}$`)
	rangeTokenRE   = regexp.MustCompile(`^[0-9]+d$`)
	nowFunc        = time.Now
	blockedStocks  = map[string]string{}
)

var errDatabentoAPIKeyNotConfigured = errors.New("databento api key not configured")

type httpStatusError struct {
	StatusCode int
	Status     string
	Body       string
}

// Error renders HTTP status details for Databento request failures.
func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d %s %s", e.StatusCode, e.Status, e.Body)
}

type StockQuote struct {
	CurrentPrice  float64 `json:"c"`
	Change        float64 `json:"d"`
	PercentChange float64 `json:"dp"`
	High          float64 `json:"h"`
	Low           float64 `json:"l"`
	Open          float64 `json:"o"`
	PreviousClose float64 `json:"pc"`
}

type CompanyProfile struct {
	Name                 string  `json:"name"`
	MarketCapitalization float64 `json:"marketCapitalization"` //nolint:tagliatelle // Finnhub response uses camelCase.
	Industry             string  `json:"finnhubIndustry"`      //nolint:tagliatelle // Finnhub response uses camelCase.
	Exchange             string  `json:"exchange"`
}

type HistoricalBar struct {
	Date   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume uint64
}

type databentoErrorPayload struct {
	Detail struct {
		Case    string           `json:"case"`
		Payload databentoPayload `json:"payload"`
	} `json:"detail"`
}

type databentoPayload struct {
	AvailableStart string `json:"available_start"`
	AvailableEnd   string `json:"available_end"`
}

func stockHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	symbol, days, err := parseStockCommand(update.Message.Text)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            err.Error(),
		})
		return
	}

	if msg, blocked := blockedStockResponse(symbol); blocked {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Text:            msg,
			ReplyParameters: &models.ReplyParameters{
				MessageID:                update.Message.ID,
				AllowSendingWithoutReply: true,
			},
		})
		return
	}

	loadingText := fmt.Sprintf("Fetching data for %s...", symbol)
	if days > 0 {
		loadingText = fmt.Sprintf("Fetching %d-day historical data for %s...", days, symbol)
	}
	loadingMsg, loadingErr := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            loadingText,
		ReplyParameters: &models.ReplyParameters{
			MessageID:                update.Message.ID,
			AllowSendingWithoutReply: true,
		},
	})
	if loadingErr != nil {
		log.Warn().Err(loadingErr).Str("symbol", symbol).Int("days", days).Msg("Failed to send stock loading state")
	}

	if days > 0 {
		handleHistoricalStock(ctx, b, update, symbol, days, loadingMsg, loadingErr)
		return
	}

	quote, err := fetchStockQuote(ctx, symbol)
	if err != nil {
		log.Error().Err(err).Str("symbol", symbol).Msg("Failed to fetch stock quote")
		sendOrEditStockResult(ctx, b, update, loadingMsg, loadingErr, fmt.Sprintf("Failed to fetch stock quote for %s. Please try again later.", symbol))
		return
	}

	profile, err := fetchCompanyProfile(ctx, symbol)
	if err != nil {
		log.Warn().Err(err).Str("symbol", symbol).Msg("Failed to fetch company profile")
	}

	sendOrEditStockResult(ctx, b, update, loadingMsg, loadingErr, formatStockMessage(symbol, quote, profile))
}

// handleHistoricalStock fetches historical bars, renders a chart, and replies
// with either a photo+caption or a text fallback.
func handleHistoricalStock(
	ctx context.Context,
	b *bot.Bot,
	update *models.Update,
	symbol string,
	days int,
	loadingMsg *models.Message,
	loadingErr error,
) {
	bars, adjustedNote, err := fetchHistoricalBars(ctx, symbol, days)
	if err != nil {
		msg := fmt.Sprintf("Failed to fetch %d-day historical data for %s. Please try again later.", days, symbol)
		if errors.Is(err, errDatabentoAPIKeyNotConfigured) {
			msg = "Historical data is unavailable: DATABENTO_API_KEY is not configured."
		}
		log.Error().Err(err).Str("symbol", symbol).Int("days", days).Msg("Failed to fetch historical bars")
		sendOrEditStockResult(ctx, b, update, loadingMsg, loadingErr, msg)
		return
	}
	if len(bars) == 0 {
		sendOrEditStockResult(ctx, b, update, loadingMsg, loadingErr, fmt.Sprintf("No historical data returned for %s in the last %d days.", symbol, days))
		return
	}

	profile, profileErr := fetchCompanyProfile(ctx, symbol)
	if profileErr != nil {
		log.Warn().Err(profileErr).Str("symbol", symbol).Msg("Failed to fetch company profile for historical stock response")
	}

	caption := formatHistoricalSummary(symbol, days, bars, profile)
	if adjustedNote != "" {
		caption += "\n" + adjustedNote
	}
	chartPNG, err := renderHistoricalChartPNG(symbol, days, bars)
	if err != nil {
		log.Warn().Err(err).Str("symbol", symbol).Int("days", days).Msg("Failed to render historical chart; sending text only")
		sendOrEditStockResult(ctx, b, update, loadingMsg, loadingErr, caption)
		return
	}

	updateStockLoadingState(ctx, b, update, loadingMsg, loadingErr, fmt.Sprintf("Fetched %d-day historical data for %s. Sending chart...", days, symbol))

	_, sendErr := b.SendPhoto(ctx, &bot.SendPhotoParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Photo: &models.InputFileUpload{
			Filename: fmt.Sprintf("%s-%dd.png", strings.ToLower(symbol), days),
			Data:     bytes.NewReader(chartPNG),
		},
		Caption: caption,
		ReplyParameters: &models.ReplyParameters{
			MessageID:                update.Message.ID,
			AllowSendingWithoutReply: true,
		},
	})
	if sendErr == nil {
		updateStockLoadingState(ctx, b, update, loadingMsg, loadingErr, fmt.Sprintf("Done. Sent %d-day chart for %s.", days, symbol))
		return
	}

	log.Warn().Err(sendErr).Str("symbol", symbol).Int("days", days).Msg("Failed to send historical chart image; sending text only")
	sendOrEditStockResult(ctx, b, update, loadingMsg, loadingErr, caption)
}

// sendOrEditStockResult edits the loading message when available, otherwise it
// falls back to sending a fresh reply to the original command.
func sendOrEditStockResult(
	ctx context.Context,
	b *bot.Bot,
	update *models.Update,
	loadingMsg *models.Message,
	loadingErr error,
	text string,
) {
	if loadingErr == nil && loadingMsg != nil {
		_, editErr := b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    update.Message.Chat.ID,
			MessageID: loadingMsg.ID,
			Text:      text,
		})
		if editErr == nil {
			return
		}
		log.Warn().Err(editErr).Int64("chat_id", update.Message.Chat.ID).Int("message_id", loadingMsg.ID).Msg("Failed to edit stock loading message; sending fallback")
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            text,
		ReplyParameters: &models.ReplyParameters{
			MessageID:                update.Message.ID,
			AllowSendingWithoutReply: true,
		},
	})
}

// updateStockLoadingState best-effort updates the loading message text and does
// not send fallback messages on edit failure.
func updateStockLoadingState(
	ctx context.Context,
	b *bot.Bot,
	update *models.Update,
	loadingMsg *models.Message,
	loadingErr error,
	text string,
) {
	if loadingErr != nil || loadingMsg == nil {
		return
	}
	_, editErr := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    update.Message.Chat.ID,
		MessageID: loadingMsg.ID,
		Text:      text,
	})
	if editErr != nil {
		log.Debug().Err(editErr).Int64("chat_id", update.Message.Chat.ID).Int("message_id", loadingMsg.ID).Msg("Failed to update stock loading state")
	}
}

// parseStockCommand parses `!s` commands and validates symbol/range inputs.
func parseStockCommand(text string) (string, int, error) {
	if strings.HasPrefix(text, "!s") && len(text) > 2 {
		if text[2] != ' ' {
			return "", 0, errors.New(invalidUsageSymbol)
		}
	}

	raw := strings.TrimSpace(strings.TrimPrefix(text, "!s"))
	if raw == "" {
		return "", 0, errors.New("please provide a stock symbol, usage: !s AAPL or !s AAPL 7d")
	}

	parts := strings.Fields(raw)
	if len(parts) > 2 {
		return "", 0, errors.New(invalidUsageSymbol)
	}

	symbol := strings.ToUpper(parts[0])
	if !symbolRegex.MatchString(symbol) {
		return "", 0, errors.New("invalid stock symbol, use 1-10 characters: letters, numbers, dots (.) or dashes (-), e.g., AAPL, BRK.A")
	}

	if len(parts) == 1 {
		return symbol, 0, nil
	}

	switch strings.ToLower(parts[1]) {
	case "7d":
		return symbol, 7, nil
	case "30d":
		return symbol, 30, nil
	default:
		if rangeTokenRE.MatchString(strings.ToLower(parts[1])) {
			return "", 0, errors.New("invalid range, use 7d or 30d (e.g., !s AAPL 7d)")
		}
		return "", 0, errors.New(invalidUsageSymbol)
	}
}

func blockedStockResponse(symbol string) (string, bool) {
	msg, ok := blockedStocks[symbol]
	return msg, ok
}

func fetchStockQuote(ctx context.Context, symbol string) (*StockQuote, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, errors.New("FINNHUB_API_KEY not configured")
	}
	u, err := url.Parse(finnhubBaseURL + "/quote")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("symbol", symbol)
	q.Set("token", apiKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	// URL is built from the trusted finnhubBaseURL constant.
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(unexpectedCodeErrMsg, resp.StatusCode)
	}

	var quote StockQuote
	if err := json.NewDecoder(resp.Body).Decode(&quote); err != nil {
		return nil, err
	}

	if quote.CurrentPrice == 0 {
		return nil, errors.New("symbol not found or no data available")
	}

	return &quote, nil
}

func fetchCompanyProfile(ctx context.Context, symbol string) (*CompanyProfile, error) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		return nil, errors.New("FINNHUB_API_KEY not configured")
	}
	u, err := url.Parse(finnhubBaseURL + "/stock/profile2")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("symbol", symbol)
	q.Set("token", apiKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	// URL is built from the trusted finnhubBaseURL constant.
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(unexpectedCodeErrMsg, resp.StatusCode)
	}

	var profile CompanyProfile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, err
	}

	return &profile, nil
}

func formatStockMessage(symbol string, quote *StockQuote, profile *CompanyProfile) string {
	changeEmoji := "🔴"
	if quote.Change >= 0 {
		changeEmoji = "🟢"
	}

	name := symbol
	var marketCapStr string
	var industryStr string

	if profile != nil && profile.Name != "" {
		name = profile.Name
		if profile.MarketCapitalization > 0 {
			marketCapStr = fmt.Sprintf("\n🏢 Market Cap: $%.2fB", profile.MarketCapitalization/1000)
		}
		if profile.Industry != "" {
			industryStr = "\n🏭 Industry: " + profile.Industry
		}
	}

	return fmt.Sprintf(`%s (%s) %s
💵 Current: $%.2f
📈 Change: %.2f (%.2f%%)
📊 Open: $%.2f | High: $%.2f | Low: $%.2f
📉 Previous Close: $%.2f%s%s`,
		name, symbol, changeEmoji,
		quote.CurrentPrice,
		quote.Change, quote.PercentChange,
		quote.Open, quote.High, quote.Low,
		quote.PreviousClose,
		marketCapStr, industryStr)
}

// fetchHistoricalBars requests Databento daily OHLCV bars and normalizes them
// into sorted, day-truncated records.
func fetchHistoricalBars(ctx context.Context, symbol string, days int) ([]HistoricalBar, string, error) {
	if days < 1 || days > 30 {
		return nil, "", errors.New("historical range must be between 1 and 30 days")
	}

	apiKey := strings.TrimSpace(os.Getenv("DATABENTO_API_KEY"))
	if apiKey == "" {
		return nil, "", errDatabentoAPIKeyNotConfigured
	}

	dataset := strings.TrimSpace(os.Getenv("DATABENTO_DATASET"))
	if dataset == "" {
		dataset = "EQUS.MINI"
	}

	dateRange := historicalDateRangeUTC(nowFunc(), days)
	params := dbn_hist.SubmitJobParams{
		Dataset:     dataset,
		Symbols:     symbol,
		Schema:      dbn.Schema_Ohlcv1D,
		DateRange:   dateRange,
		Encoding:    dbn.Encoding_Dbn,
		Compression: dbn.Compress_None,
		StypeIn:     dbn.SType_RawSymbol,
	}

	adjustedNote := ""
	raw, err := getHistoricalRangeWithContext(ctx, apiKey, &params)
	if err != nil {
		// Single-retry path: only retry once when Databento reports that the
		// requested end date is newer than the dataset's available end date.
		if retryParams, ok := tryAdjustRangeFromDatabento422(&params, err, days); ok {
			adjustedNote = fmt.Sprintf(
				"Note: data availability lagged; used latest available window ending %s UTC.",
				retryParams.DateRange.End.Format(dateFormatPattern),
			)
			raw, err = getHistoricalRangeWithContext(ctx, apiKey, &retryParams)
		}
	}
	if err != nil {
		return nil, "", err
	}

	records, _, err := dbn.ReadDBNToSlice[dbn.OhlcvMsg](bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}

	bars := make([]HistoricalBar, 0, len(records))
	for _, rec := range records {
		if rec.Header.TsEvent > uint64(math.MaxInt64) {
			return nil, "", errors.New("invalid timestamp from historical data")
		}
		ts := time.Unix(0, int64(rec.Header.TsEvent)).UTC().Truncate(24 * time.Hour)
		bars = append(bars, HistoricalBar{
			Date:   ts,
			Open:   float64(rec.Open) / 1e9,
			High:   float64(rec.High) / 1e9,
			Low:    float64(rec.Low) / 1e9,
			Close:  float64(rec.Close) / 1e9,
			Volume: rec.Volume,
		})
	}

	sort.Slice(bars, func(i, j int) bool {
		return bars[i].Date.Before(bars[j].Date)
	})
	return bars, adjustedNote, nil
}

// historicalDateRangeUTC returns a UTC midnight-aligned half-open date range.
// The end is set to the previous day so the range stays within Databento's
// historical (non-live) data boundary.
func historicalDateRangeUTC(now time.Time, days int) dbn_hist.DateRange {
	end := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	return dbn_hist.DateRange{
		Start: end.AddDate(0, 0, -days),
		End:   end,
	}
}

// getHistoricalRangeWithContext performs a context-aware Databento
// timeseries.get_range request and returns raw DBN bytes.
func getHistoricalRangeWithContext(ctx context.Context, apiKey string, params *dbn_hist.SubmitJobParams) ([]byte, error) {
	formData := url.Values{}
	if err := params.ApplyToURLValues(&formData); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://hist.databento.com/v0/timeseries.get_range",
		strings.NewReader(formData.Encode()),
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/octet-stream")
	req.SetBasicAuth(apiKey, "")

	// Request URL is a trusted Databento constant; user input is only form-encoded parameters.
	resp, err := histHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       string(body),
		}
	}
	return body, nil
}

// tryAdjustRangeFromDatabento422 shifts the query window into Databento's
// available schema range for supported 422 cases, allowing one safe retry.
func tryAdjustRangeFromDatabento422(params *dbn_hist.SubmitJobParams, err error, days int) (dbn_hist.SubmitJobParams, bool) {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusUnprocessableEntity {
		return *params, false
	}

	var payload databentoErrorPayload
	if json.Unmarshal([]byte(statusErr.Body), &payload) != nil {
		return *params, false
	}
	switch payload.Detail.Case {
	case "data_end_after_available_end", "data_schema_not_fully_available":
	default:
		return *params, false
	}

	if strings.TrimSpace(payload.Detail.Payload.AvailableEnd) == "" {
		return *params, false
	}

	availableEnd, parseErr := time.Parse(time.RFC3339Nano, payload.Detail.Payload.AvailableEnd)
	if parseErr != nil {
		return *params, false
	}
	availableEnd = availableEnd.UTC().Truncate(24 * time.Hour)
	adjusted := *params
	adjusted.DateRange.End = availableEnd
	adjusted.DateRange.Start = availableEnd.AddDate(0, 0, -days)

	if strings.TrimSpace(payload.Detail.Payload.AvailableStart) != "" {
		availableStart, startParseErr := time.Parse(time.RFC3339Nano, payload.Detail.Payload.AvailableStart)
		if startParseErr == nil {
			availableStart = availableStart.UTC().Truncate(24 * time.Hour)
			if adjusted.DateRange.Start.Before(availableStart) {
				adjusted.DateRange.Start = availableStart
			}
		}
	}

	if !adjusted.DateRange.Start.Before(adjusted.DateRange.End) {
		return *params, false
	}
	return adjusted, true
}

// formatHistoricalSummary creates a compact caption for historical responses.
func formatHistoricalSummary(symbol string, days int, bars []HistoricalBar, profile *CompanyProfile) string {
	if len(bars) == 0 {
		return fmt.Sprintf("No historical data returned for %s in the last %d days.", symbol, days)
	}

	first := bars[0]
	last := bars[len(bars)-1]
	high := bars[0].High
	low := bars[0].Low
	for _, bar := range bars[1:] {
		high = math.Max(high, bar.High)
		low = math.Min(low, bar.Low)
	}

	change := 0.0
	if first.Close != 0 {
		change = (last.Close - first.Close) / first.Close * 100
	}
	title := symbol
	marketCapStr := ""
	industryStr := ""
	if profile != nil {
		if profile.Name != "" {
			title = profile.Name + " (" + symbol + ")"
		}
		if profile.MarketCapitalization > 0 {
			marketCapStr = fmt.Sprintf("\n🏢 Market Cap: $%.2fB", profile.MarketCapitalization/1000)
		}
		if profile.Industry != "" {
			industryStr = "\n🏭 Industry: " + profile.Industry
		}
	}

	return fmt.Sprintf(
		"%s %dd (%s to %s)\nClose: $%.2f\nReturn: %.2f%%\nRange: $%.2f - $%.2f%s%s",
		title,
		days,
		first.Date.Format(dateFormatPattern),
		last.Date.Format(dateFormatPattern),
		last.Close,
		change,
		low,
		high,
		marketCapStr,
		industryStr,
	)
}

// renderHistoricalChartPNG renders close prices as a PNG line chart.
func renderHistoricalChartPNG(symbol string, days int, bars []HistoricalBar) ([]byte, error) {
	values := make([]float64, 0, len(bars))
	labels := make([]string, 0, len(bars))
	for _, bar := range bars {
		values = append(values, bar.Close)
		labels = append(labels, bar.Date.Format("01-02"))
	}

	p, err := charts.LineRender(
		[][]float64{values},
		charts.TitleTextOptionFunc(fmt.Sprintf("%s %dD Close", symbol, days)),
		charts.LegendLabelsOptionFunc([]string{symbol}),
		charts.XAxisLabelsOptionFunc(labels),
		func(opt *charts.ChartOption) {
			opt.Symbol = charts.SymbolCircle
			opt.LineStrokeWidth = 1.2
			opt.ValueFormatter = func(f float64) string {
				return fmt.Sprintf("%.2f", f)
			}
		},
	)
	if err != nil {
		return nil, err
	}
	return p.Bytes()
}
