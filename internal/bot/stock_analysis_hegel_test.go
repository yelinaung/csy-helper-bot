package bot

import (
	"encoding/json"
	"math"
	"testing"

	"hegel.dev/go/hegel"
)

// TestPriceTargetToSanitized_AlwaysFiniteAndMarshallable is a boundary
// PBT that draws floats from hegel.Floats[float64]() — which includes
// +Inf, -Inf, and NaN by default — and asserts:
//   - UpsidePct is always finite (catches Bug 1: +Inf TargetMean slipped
//     past the > 0 guard and produced +Inf UpsidePct).
//   - json.Marshal never errors (catches the pass-through fields too:
//     TargetHigh/Low/Mean/Median/CurrentPrice are copied directly and
//     also break json.Marshal when non-finite).
//   - When both inputs are finite and > 0 and the quotient itself is
//     finite, UpsidePct matches the formula (TargetMean/currentPrice - 1)
//     times 100 within float tolerance. When the quotient overflows to
//     +Inf, UpsidePct is 0 (the result guard drops it).
//   - Nil pointer returns nil.
func TestPriceTargetToSanitized_AlwaysFiniteAndMarshallable(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		targetHigh := hegel.Draw(ht, hegel.Floats[float64]())
		targetLow := hegel.Draw(ht, hegel.Floats[float64]())
		targetMean := hegel.Draw(ht, hegel.Floats[float64]())
		targetMedian := hegel.Draw(ht, hegel.Floats[float64]())
		currentPrice := hegel.Draw(ht, hegel.Floats[float64]())
		quoteCP := hegel.Draw(ht, hegel.Floats[float64]())

		// Property: nil pointer returns nil.
		if got := priceTargetToSanitized(nil, currentPrice); got != nil {
			ht.Fatalf("priceTargetToSanitized(nil, %v) = %v, want nil", currentPrice, got)
		}

		pt := &PriceTarget{
			TargetHigh:   targetHigh,
			TargetLow:    targetLow,
			TargetMean:   targetMean,
			TargetMedian: targetMedian,
			CurrentPrice: currentPrice,
		}
		got := priceTargetToSanitized(pt, quoteCP)
		// Property: non-nil input returns non-nil output.
		// Use panic instead of ht.Fatal so staticcheck SA5011
		// recognizes this as a terminator and doesn't flag the
		// dereferences below as possible nil pointer dereferences.
		// ht.Fatal is a custom type method that staticcheck doesn't
		// know terminates execution.
		if got == nil {
			panic("priceTargetToSanitized returned nil for non-nil input")
		}

		// Property: UpsidePct is never NaN.
		if math.IsNaN(got.UpsidePct) {
			ht.Fatalf("UpsidePct is NaN for mean=%v current=%v quoteCP=%v",
				targetMean, currentPrice, quoteCP)
		}

		// Property: UpsidePct is never Inf.
		if math.IsInf(got.UpsidePct, 0) {
			ht.Fatalf("UpsidePct is Inf for mean=%v current=%v quoteCP=%v",
				targetMean, currentPrice, quoteCP)
		}

		// Property: no pass-through field is non-finite (the sanitizeFiniteFloat
		// helper coerces them to 0).
		for name, v := range map[string]float64{
			"TargetHigh": got.TargetHigh, "TargetLow": got.TargetLow,
			"TargetMean": got.TargetMean, "TargetMedian": got.TargetMedian,
			"CurrentPrice": got.CurrentPrice,
		} {
			if math.IsInf(v, 0) || math.IsNaN(v) {
				ht.Fatalf("%s is non-finite: %v (input mean=%v cp=%v quoteCP=%v)",
					name, v, targetMean, currentPrice, quoteCP)
			}
		}

		// Property: output JSON always marshals successfully.
		data, err := json.Marshal(got)
		if err != nil {
			ht.Fatalf("marshal sanitizedPriceTarget failed: %v "+
				"(mean=%v cp=%v quoteCP=%v high=%v low=%v median=%v)",
				err, targetMean, currentPrice, quoteCP,
				targetHigh, targetLow, targetMedian)
		}
		// And the JSON is valid.
		var check map[string]any
		if err := json.Unmarshal(data, &check); err != nil {
			ht.Fatalf("unmarshal sanitizedPriceTarget failed: %v", err)
		}

		// Property: when effective price > 0 and targetMean > 0, both
		// finite, and the quotient is finite, UpsidePct matches the
		// formula. When the quotient overflows to +Inf, UpsidePct is 0.
		effectivePrice := quoteCP
		if effectivePrice <= 0 {
			effectivePrice = currentPrice
		}
		if effectivePrice > 0 && targetMean > 0 &&
			!math.IsInf(effectivePrice, 0) && !math.IsNaN(effectivePrice) &&
			!math.IsInf(targetMean, 0) && !math.IsNaN(targetMean) {
			expected := (targetMean/effectivePrice - 1) * 100
			if !math.IsInf(expected, 0) && !math.IsNaN(expected) {
				// Relative tolerance: at magnitudes like 1e209 the float64
				// ULP dwarfs any absolute epsilon, and codegen differences
				// between call sites can shift the last bits.
				if math.Abs(got.UpsidePct-expected) > 1e-9*max(1, math.Abs(expected)) {
					ht.Fatalf("UpsidePct mismatch: got %v, want ~%v "+
						"(mean=%v effectivePrice=%v)",
						got.UpsidePct, expected, targetMean, effectivePrice)
				}
			} else {
				// Quotient overflowed — result guard must drop it to 0.
				if got.UpsidePct != 0 {
					ht.Fatalf("UpsidePct should be 0 on quotient overflow, "+
						"got %v (mean=%v effectivePrice=%v expected=%v)",
						got.UpsidePct, targetMean, effectivePrice, expected)
				}
			}
		}
	}, hegel.WithTestCases(200))
}
