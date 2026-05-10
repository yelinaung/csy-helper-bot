package bot

import (
	"strings"
	"testing"
)

func TestParseStockAnalysisCommand(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantSym   string
		wantError bool
		errSubstr string
	}{
		{name: "valid symbol", input: "!sa AAPL", wantSym: testSymbolAAPL},
		{name: "lowercase symbol uppercased", input: "!sa aapl", wantSym: testSymbolAAPL},
		{name: "symbol with dot", input: "!sa BRK.A", wantSym: "BRK.A"},
		{name: "symbol with hyphen", input: "!sa BF-B", wantSym: "BF-B"},
		{name: "empty command", input: "!sa", wantError: true, errSubstr: "please provide"},
		{name: "only spaces after command", input: "!sa   ", wantError: true, errSubstr: "please provide"},
		{name: "tab after command", input: "!sa\tAAPL", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "newline after command", input: "!sa\nAAPL", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "missing separator", input: "!saAAPL", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "historical range 7d rejected", input: "!sa AAPL 7d", wantError: true, errSubstr: "does not support historical ranges"},
		{name: "historical range 30d rejected", input: "!sa AAPL 30d", wantError: true, errSubstr: "does not support historical ranges"},
		{name: "historical range 60d rejected", input: "!sa AAPL 60d", wantError: true, errSubstr: "does not support historical ranges"},
		{name: "historical range 90d rejected", input: "!sa AAPL 90d", wantError: true, errSubstr: "does not support historical ranges"},
		{name: "invalid range 1d rejected", input: "!sa AAPL 1d", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "invalid range 10d rejected", input: "!sa AAPL 10d", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "extra token rejected", input: "!sa AAPL foobar", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "multiple extra tokens rejected", input: "!sa AAPL x y", wantError: true, errSubstr: testErrInvalidUsage},
		{name: "invalid symbol chars", input: "!sa $$$", wantError: true, errSubstr: "invalid stock symbol"},
		{name: "invalid symbol too long", input: "!sa ABCDEFGHIJK", wantError: true, errSubstr: "invalid stock symbol"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSym, err := parseStockAnalysisCommand(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error, got symbol=%q", gotSym)
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotSym != tt.wantSym {
				t.Fatalf("got %q, want %q", gotSym, tt.wantSym)
			}
		})
	}
}

func TestRouting_SA_DoesNotTrigger_StockHandler(t *testing.T) {
	// !sa AAPL should NOT be parsed by parseStockCommand.
	_, _, err := parseStockCommand("!sa AAPL")
	if err == nil {
		t.Fatal("expected !sa AAPL to fail parseStockCommand")
	}
	if !strings.Contains(err.Error(), testErrInvalidUsage) {
		t.Fatalf("expected 'invalid usage' error, got %q", err.Error())
	}

	// !sa without space should also fail.
	_, _, err = parseStockCommand("!saAAPL")
	if err == nil {
		t.Fatal("expected !saAAPL to fail parseStockCommand")
	}

	// !sa alone should also fail.
	_, _, err = parseStockCommand("!sa")
	if err == nil {
		t.Fatal("expected !sa to fail parseStockCommand")
	}
}
