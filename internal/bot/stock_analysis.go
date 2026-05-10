package bot

import (
	"errors"
	"strings"
)

const (
	analysisInvalidUsageMsg = "invalid usage, use !sa SYMBOL (e.g., !sa AAPL)"
)

// parseStockAnalysisCommand parses !sa commands and validates symbol input.
// It rejects any second token, with a specialized error for known range
// suffixes.
func parseStockAnalysisCommand(text string) (string, error) {
	symbol, parts, err := extractSymbolToken(text, "!sa", analysisInvalidUsageMsg)
	if err != nil {
		return "", err
	}

	if len(parts) > 1 {
		if isKnownRangeToken(parts[1]) {
			return "", errors.New("stock analysis does not support historical ranges. Use !sa SYMBOL (e.g., !sa AAPL)")
		}
		return "", errors.New(analysisInvalidUsageMsg)
	}

	return symbol, nil
}

func isKnownRangeToken(token string) bool {
	switch strings.ToLower(token) {
	case "7d", "30d", "60d", "90d":
		return true
	default:
		return false
	}
}
