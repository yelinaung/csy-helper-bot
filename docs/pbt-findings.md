# PBT Findings & Opportunities

A review of `internal/bot/` surfaced four confirmed bugs and a set of
property-based testing (PBT) opportunities for [Hegel](https://hegel.dev).
The repo already has 16 native Go `Fuzz*` tests in
`internal/bot/fuzz_test.go`; the opportunities below add **new** coverage and
would catch bugs the existing tests miss.

## Confirmed bugs

A temporary repro test (since deleted) confirmed each bug against the
current source. The repro output under each bug shows the concrete failure
mode.

### Bug 1 — `priceTargetToSanitized` emits `+Inf`, breaking JSON marshal

- **Location:** `internal/bot/stock_analysis.go:417`
- **Symptom:** When `pt.TargetMean` is `+Inf` and `currentPrice > 0`,
  `UpsidePct` becomes `+Inf`. The subsequent `json.MarshalIndent` at
  `stock_analysis.go:431` fails with `json: unsupported value: +Inf`,
  aborting the entire `!sa` prompt build.
- **Root cause:** The guard is `if currentPrice > 0 && pt.TargetMean > 0`.
  `+Inf > 0` is true, so the guard passes. `NaN > 0` and `-Inf > 0` are
  false, so they accidentally produce `UpsidePct = 0` and are safe — but
  `+Inf` is not.
- **Repro output:**
  ```
  UpsidePct = +Inf  isInf=true  isNaN=false
  BUG CONFIRMED: UpsidePct is non-finite; json.Marshal would error.
  TargetMean=-Inf -> UpsidePct=0  isInf=false  isNaN=false
  TargetMean=NaN  -> UpsidePct=0  isInf=false  isNaN=false
  ```
  And `json.Marshal` of any struct containing `+Inf`/`-Inf`/`NaN`:
  ```
  v=+Inf  err=json: unsupported value: +Inf
  v=-Inf  err=json: unsupported value: -Inf
  v=NaN   err=json: unsupported value: NaN
  ```
- **Why the existing test misses it:** `FuzzPriceTargetUpsidePct`
  (`fuzz_test.go:415`) asserts `!IsInf` and that `json.Marshal` succeeds, but
  Go's native fuzzer rarely generates `+Inf` from `float64` bit-flipping.
  Hegel's `hegel.Floats[float64]()` includes Inf and NaN by default and
  would surface this on the first run.
- **Impact:** Any `!sa` request where Finnhub returns a `+Inf` target (or
  where upstream parsing produces one) fails with a generic
  "Failed to analyze %s" message instead of degrading gracefully.
- **Fix:** Guarding only the inputs to `UpsidePct` is insufficient on two
  fronts. First, `priceTargetToSanitized` copies `TargetHigh`, `TargetLow`,
  `TargetMean`, `TargetMedian`, and `CurrentPrice` directly into the
  returned struct (lines 411-415), and any non-finite value in those fields
  also breaks `json.Marshal`. Second, even when both inputs to the division
  are finite and `> 0`, the quotient can overflow to `+Inf` — e.g.
  `math.MaxFloat64 / math.SmallestNonzeroFloat64` — so guarding the inputs
  does not guarantee a finite result. Guard the **result**, not just the
  inputs, and coerce non-finite pass-through fields to 0 at copy time:

  ```go
  // import "math"  // add to imports — stock_analysis.go does not import it
  sanitizeFloat := func(f float64) float64 {
      if math.IsInf(f, 0) || math.IsNaN(f) {
          return 0
      }
      return f
  }
  spt := &sanitizedPriceTarget{
      TargetHigh:   sanitizeFloat(pt.TargetHigh),
      TargetLow:    sanitizeFloat(pt.TargetLow),
      TargetMean:   sanitizeFloat(pt.TargetMean),
      TargetMedian: sanitizeFloat(pt.TargetMedian),
      CurrentPrice: sanitizeFloat(currentPrice),
  }
  if currentPrice > 0 && pt.TargetMean > 0 {
      up := (pt.TargetMean/currentPrice - 1) * 100
      if !math.IsInf(up, 0) && !math.IsNaN(up) {
          spt.UpsidePct = up
      }
  }
  ```

  Rationale for each check:
  - `sanitizeFloat` on the pass-through fields: those have no `> 0` guard,
    so `NaN`, `+Inf`, and `-Inf` all reach `json.Marshal` and must be
    coerced.
  - `> 0` on `currentPrice` and `pt.TargetMean`: excludes `NaN` and `-Inf`
    (both compare false to `> 0`), so only finite positive values (plus
    `+Inf`, which `sanitizeFloat` already handled on the pass-through) reach
    the division.
  - `!math.IsInf(up, 0) && !math.IsNaN(up)` on the **result**: catches the
    overflow-to-`+Inf` case from huge-but-finite inputs. `IsNaN` on the
    result is technically unreachable when both operands are finite and
    `> 0`, but it's cheap insurance and keeps the code honestly matching
    the "always finite" property asserted by opportunity #2 below —
    without it, the PBT and the fix disagree.

  Consider applying the same `sanitizeFloat` helper to every float that
  flows into `analysisPromptPayload` (`sanitizedQuote`,
  `sanitizedMetrics`, `sanitizedProfile.MarketCapB`).

### Bug 2 — `mentionAndSuffixFromText` misses mentions after case-shifting chars

- **Location:** `internal/bot/ask.go:411` (with `isTelegramUsernameChar`
  at `ask.go:449`).
- **Symptom:** A bare `@bot_mention` preceded by a character whose
  lowercase form has a different UTF-8 byte length is silently not
  recognized. The handler then treats the message as not addressing the
  bot and drops it.
- **Root cause:** The function lowercases the entire text with
  `strings.ToLower`, searches for the lowercase mention in `lowerText`,
  then maps the byte offsets back into the original `text`.
  `strings.ToLower` is **not byte-length-preserving**:
  - `ẞ` U+1E9E (3 bytes) → `ß` U+00DF (2 bytes)
  - `İ` U+0130 (2 bytes) → `i̇` (3 bytes, combining dot)

  After the shift, `text[start:end]` no longer aligns with the mention,
  `hasMentionBoundaries` is called with wrong indices, and the match is
  rejected.
- **Repro output:**
  ```
  capital-sharp-s prefix ok=false  mention=""  suffix=""
  capital-I-dot prefix   ok=false  mention=""  suffix=""
  ascii baseline         ok=true   mention="@csy_helper_dev_bot"  suffix=""
  ```
- **Why the existing test misses it:** `FuzzMentionAndSuffixAtEntity` and
  `FuzzShouldHandleAskMention` (`fuzz_test.go:56`, `:77`) operate on the
  entity-based path (`mentionAndSuffixAtEntity`), which uses
  `utf16EntityRangeToByteRange` and is correct. The fallback path
  (`mentionAndSuffixFromText`) is only reached when Telegram omits the
  mention entity, and no current test exercises it with Unicode prefixes.
- **Impact:** Users in locales that use `İ`, `ẞ`, and similar characters
  cannot address the bot when the mention entity is missing. The bug is
  silent — no error, just a dropped message.
- **Fix:** Search in `text` directly with a case-insensitive scan that
  recomputes indices against `text` rather than `lowerText`. Or, simpler:
  walk `text` rune-by-rune and compare lowercased runes against the
  lowercased mention, recording byte offsets in the original string.

### Bug 3 — `memoryRateLimiter.allow` returns `retryAfter > window` on clock skew

- **Location:** `internal/bot/rate_limiter.go:72`
- **Symptom:** When the clock moves backwards (NTP step, container clock
  jump, non-monotonic `time.Now()`), `retryAfter` exceeds `r.window`.
  The caller surfaces this to the user as "try again in 15s" for a 10s
  window.
- **Root cause:**
  ```go
  retryAfter := r.window - now.Sub(entry.windowStart)
  retryAfter = max(retryAfter, 0)
  ```
  `max(..., 0)` floors but does not cap. If `now` is before
  `entry.windowStart`, `now.Sub(...)` is negative and `retryAfter` becomes
  `window + |skew|`.
- **Repro output:**
  ```
  second at t0: ok=false retry=10s
  clock-skew third: ok=false retry=15s  window=10s  retry>window=true
  BUG CONFIRMED: retryAfter (15s) > window (10s) on backwards clock.
  ```
- **Why the existing test misses it:** `TestMemoryRateLimiterAllow` and
  the sweep tests always move time forward. No current test injects a
  backwards clock.
- **Impact:** Misleading user-facing retry durations. Not a crash, but a
  correctness bug in the rate-limit contract.
- **Fix:** `retryAfter = clamp(retryAfter, 0, r.window)` — i.e.
  `min(max(retryAfter, 0), r.window)`. Consider also documenting that
  callers should pass a monotonic clock.

### Bug 4 — `plainTelegramMarkdownText` leaks NUL bytes and invalid UTF-8

- **Location:** `internal/bot/telegram_markdown.go:115` (called from
  `stock_analysis.go:624`).
- **Symptom:** The plain-text fallback returns untrusted model output with
  NUL bytes and invalid UTF-8 sequences passed through unchanged. Its
  sibling `formatTelegramMarkdown` (`telegram_markdown.go:22`) sanitizes
  both up front with `strings.ToValidUTF8` + NUL stripping (lines 25-26,
  commented "so Telegram never receives malformed text"); the plain path
  skips that step entirely and goes straight to
  `normalizeGeneratedTelegramMarkdown` + regex replacement.
- **Root cause:** Missing the `strings.ToValidUTF8(text, "�")` and
  `strings.ReplaceAll(text, "\x00", "")` calls that the markdown path
  performs.
- **Repro output:**
  ```
  in  = "hello\x00world\xff bad"
  plainTelegramMarkdownText: nul=true  validUTF8=false  out="hello\x00world\xff bad"
  formatTelegramMarkdown:    nul=false validUTF8=true
  BUG: plainTelegramMarkdownText leaks NUL byte
  BUG: plainTelegramMarkdownText leaks invalid UTF-8
  ```
- **Why the existing test misses it:** `FuzzFormatAndNormalizeMarkdown`
  (`fuzz_test.go:375`) asserts the no-NUL / valid-UTF-8 contract for
  `formatTelegramMarkdown` (lines 392-397) but never exercises
  `plainTelegramMarkdownText`. The lone unit test
  `TestPlainTelegramMarkdownText` (`telegram_markdown_test.go:20`) checks
  one well-formed input — it doesn't cover NUL bytes, invalid UTF-8, or
  the safety contract at all.
- **Impact:** Malformed bytes can reach Telegram on the plain-text
  rendering path, which Telegram may reject or mis-render. Same class of
  bug the markdown path was hardened against; the two formatters are
  inconsistent.
- **Fix:** Apply the same sanitization at the top of
  `plainTelegramMarkdownText`:
  ```go
  text = strings.ToValidUTF8(text, "�")
  text = strings.ReplaceAll(text, "\x00", "")
  ```
  Better, factor the sanitize-prelude into a shared helper both formatters
  call so they can't drift again.

## Hegel PBT opportunities

The 16 existing `Fuzz*` tests in `fuzz_test.go` are already property tests
and are not listed here. Everything below is **new** coverage. Order within
each tier is by expected value.

### Tier 1 — high value

#### 1. Stateful model test for `memoryRateLimiter`

- **Target:** `internal/bot/rate_limiter.go` — `allow(key, now)` and
  `sweepLocked(now)` over `map[string]rateEntry`.
- **Why:** This is the single best PBT target in the repo. It's a textbook
  state machine: insert, increment, expire, sweep, with a capacity cap.
  A stateful model test exercises combinations of operations that
  hand-written tests never do.
- **Model:** A `map[string]modelEntry` where `modelEntry{windowStart time.Time;
  count int}`. In Go Hegel, rule methods take exactly one `hegel.TestCase`
  parameter; values are drawn inside the rule body, not passed as arguments.
  ```go
  // RuleAllow increments or resets a key's window.
  func (m *rateLimiterMachine) RuleAllow(tc hegel.TestCase) {
      key := hegel.Draw(tc, hegel.SampledFrom([]string{"a","b","c","d"}))
      now := m.baseTime.Add(time.Duration(hegel.Draw(tc, hegel.Integers(-3600, 3600))) * time.Second)
      ok, retry := m.subject.allow(key, now)
      m.applyModel(key, now)  // update the reference map
      if !m.agrees(key, ok, retry) {
          panic("subject and model disagree")
      }
  }

  // RuleSweep prunes expired entries.
  func (m *rateLimiterMachine) RuleSweep(tc hegel.TestCase) {
      now := m.baseTime.Add(time.Duration(hegel.Draw(tc, hegel.Integers(-3600, 3600))) * time.Second)
      m.subject.mu.Lock()
      m.subject.sweepLocked(now)
      m.subject.mu.Unlock()
      m.pruneModel(now)
  }
  ```
- **Invariants:**
  - `0 <= retryAfter <= r.window` (fails today → bug 3).
  - A key just reset (new window) has `count == 1` in the subject.
  - `len(r.data) <= rateLimitMaxMapSize` after any rule.
  - Subject and model agree on `(ok, retryAfter, count)` after every rule.
- **API:** `hegel.RunStateful(ht, machine)`. See the Go reference's
  stateful-testing section.
- **Generator notes:** Draw `now` as `baseTime + hegel.Integers(-3600, 3600)
  * time.Second` so the clock moves both ways. Draw `key` from
  `hegel.SampledFrom([]string{"a","b","c","d"})` so collisions happen
  often enough to exercise the increment path.

#### 2. Boundary PBT for `priceTargetToSanitized` (and siblings)

- **Target:** `internal/bot/stock_analysis.go:402`.
- **Why:** Port `FuzzPriceTargetUpsidePct` but with
  `hegel.Floats[float64]()` (Inf/NaN included by default). Catches bug 1
  on the first run.
- **Properties:**
  - `UpsidePct` is always finite (`!IsNaN && !IsInf`).
  - `json.Marshal(priceTargetToSanitized(pt, cp))` never errors (covers
    both `UpsidePct` and the pass-through `TargetHigh`/`Low`/`Mean`/
    `Median`/`CurrentPrice` fields, which the fix in Bug 1 coerces via
    `sanitizeFloat`).
  - When `currentPrice > 0 && TargetMean > 0`, both are finite, and the
    quotient `(TargetMean/currentPrice - 1) * 100` is itself finite,
    `UpsidePct` equals that quotient within float tolerance. When the
    quotient overflows to `+Inf` (e.g. `MaxFloat64 / SmallestNonzeroFloat64`),
    `UpsidePct` is 0 — the result guard drops it.
  - Nil pointer returns nil.
- **Generalize:** The same finiteness-and-marshal property applies to
  every float field that flows into `analysisPromptPayload`:
  `sanitizeMetrics`, `sanitizedQuote`, `sanitizedProfile.MarketCapB`.
  One parameterized PBT can cover all of them.

#### 3. Soundness PBT for `mentionAndSuffixFromText`

- **Target:** `internal/bot/ask.go:411`.
- **Why:** Catches bug 2. The function promises to find `targetMention`
  with non-username-char boundaries; it silently fails for Unicode
  prefixes that change byte length under `ToLower`.
- **Property:** For `text = prefix + mention + suffix` where `prefix` and
  `suffix` are arbitrary Unicode from `hegel.Text()` and `prefix` does not
  end with an `isTelegramUsernameChar` and `suffix` does not start with
  one, `mentionAndSuffixFromText(text, mention)` returns
  `(mention, suffix, true)`.
- **Generator:** Build the text inline:
  ```go
  mention := hegel.Draw(ht, hegel.Just("@csy_helper_dev_bot"))
  prefix  := hegel.Draw(ht, hegel.Text().MaxSize(20))
  suffix  := hegel.Draw(ht, hegel.Text().MaxSize(20))
  ht.Assume(!strings.HasSuffix(prefix, "_") && /* etc. */)
  text := prefix + mention + suffix
  ```
  Use full `hegel.Text()` (not ASCII) so `İ`, `ẞ`, combining marks, and
  emoji all appear.
- **Commutativity add-on:** The result should not depend on bytes before
  the mention. Generate two prefixes of equal "boundary class" and assert
  the function returns the same `mention` and `suffix`.

#### 4. Roundtrip PBT for `utf16EntityRangeToByteRange`

- **Target:** `internal/bot/ask.go:453`.
- **Why:** The existing `FuzzUTF16EntityRangeToByteRange`
  (`fuzz_test.go:38`) only checks the returned range is *valid*
  (`0 <= start <= end <= len(text)`), not *correct*. A roundtrip property
  verifies correctness.
- **Property:** Pick a substring `sub` of `text`. Compute its UTF-16
  offset and length by walking `text` with `utf16UnitsForRune`. Call
  `utf16EntityRangeToByteRange(text, offset, length)` and assert
  `text[start:end] == sub`.
- **Generator:** Generate `text` with `hegel.Text()` (full Unicode, so
  supplementary-plane characters that take 2 UTF-16 units are exercised).
  Draw `startRune` and `endRune` as rune indices (`0 <= startRune <=
  endRune <= runeCount(text)`), then convert to byte offsets in `text` to
  derive `sub = text[startByte:endByte]`. UTF-16 entity ranges always
  correspond to rune boundaries; generating raw byte offsets can land
  inside a multi-byte rune and produce an unsatisfiable property. Finally,
  compute the UTF-16 offset/length by walking `text[:startByte]` and
  `sub` with `utf16UnitsForRune`.

#### 5. Idempotence PBT for `sanitizeForPrompt` and the `sanitize*Results` functions

- **Targets:**
  - `sanitizeForPrompt` (`gemini_explainer.go:578`)
  - `sanitizeExaResults` (`exa_search.go:166`)
  - `sanitizeParallelResults` (`parallel_search.go:154`)
- **Why:** All three are normalization functions. Idempotence
  (`f(f(x)) == f(x)`) is the cheapest, highest-signal property for
  normalizers and is not currently asserted for any of them.
- **Properties:**
  - `sanitizeForPrompt(sanitizeForPrompt(s, n), n) ==
    sanitizeForPrompt(s, n)` for all `s` and all `n >= 0`.
  - `sanitizeExaResults(sanitizeExaResults(rs)) ==
    sanitizeExaResults(rs)`.
  - Same for `sanitizeParallelResults`.
  - Output is always valid UTF-8 with no NUL bytes.
  - Per-field rune budgets hold after sanitization.
- **Generators:** `hegel.Text()` for strings; for result slices, build
  `[]exaSearchResult` inline with `hegel.Text()` fields and
  `hegel.Lists(hegel.Text())` for highlights/excerpts.

#### 6. Roundtrip PBT for `parseStockCommand` / `parseStockAnalysisCommand`

- **Targets:** `internal/bot/stock.go:311`, `internal/bot/stock_analysis.go:168`.
- **Why:** The existing table-driven tests cover fixed examples. A PBT
  covers the full symbol grammar and arbitrary junk inputs.
- **Properties:**
  - For any symbol matching `^[A-Z0-9.\-]{1,10}$` (generate with
    `hegel.FromRegex("[A-Z0-9.\\-]{1,10}", true)` then uppercase),
    `parseStockCommand("!s " + sym)` returns `(sym, 0, nil)`.
  - For any valid symbol and range in `{7d, 30d, 60d, 90d}`,
    `parseStockCommand("!s " + sym + " " + range)` returns
    `(sym, days[range], nil)`.
  - `parseStockAnalysisCommand("!sa " + sym)` returns `(sym, nil)`.
  - No-crash: for arbitrary `hegel.Text()` input, neither parser panics.
- **Generator:** Use `hegel.FromRegex` for valid symbols and
  `hegel.Text()` for the junk-input case.

#### 7. Roundtrip PBT for `parseAllowedGroupIDs`

- **Target:** `internal/bot/bot.go:181`.
- **Why:** Round-trip with arbitrary whitespace and ordering is the
  actual contract; the existing test only checks two fixed inputs.
- **Properties:**
  - For any `[]int64` `ids`, join with `", "` plus arbitrary surrounding
    whitespace per token; `parseAllowedGroupIDs(joined)` returns a map
    whose key set equals `ids` (order-independent, duplicates collapse).
  - No-crash: arbitrary `hegel.Text()` never panics and returns either a
    map or an error.
- **Generator:** `hegel.Lists(hegel.Integers[int64](math.MinInt64,
  math.MaxInt64)).MaxSize(10)` for the id list; `hegel.Text().Alphabet("
  ,\t\n")` for the separator noise.

### Tier 2 — solid

#### 8. Commutativity / monotonicity for `shouldRespondInBurmese`

- **Target:** `internal/bot/ask.go:316`.
- **Properties:**
  - `f(a, b) == f(b, a)` (commutativity over the variadic args).
  - `f(a) ⇒ f(a, anything)` (adding more text can't flip a true result
    to false).
  - `f(a) == f(a, a)` (idempotence).
- **Generator:** `hegel.Text()` for each arg. Include Myanmar-block
  codepoints via `hegel.Text().IncludeCharacters("\u1000\u109f")` in some
  cases, or just rely on full-Unicode `Text()` to occasionally produce
  them.

#### 9. Date-arithmetic invariant for `historicalDateRangeUTC`

- **Target:** `internal/bot/stock.go:527`.
- **Properties:**
  - `End.Sub(Start) == days * 24 * time.Hour` for all `days` in `[1, 90]`.
  - `End == now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)`.
  - `Start.Before(End)`.
- **Generator:** `hegel.Integers(1, 90)` for `days` plus boundary values
  (`0`, `1`, `90`, `91`, `-1`) via `hegel.OneOf`. Draw `now` from
  `hegel.Datetimes()` to exercise time zones.

#### 10. Env-parser bounds contracts

- **Targets:**
  - `loadExaNumResults` (`exa_search.go:190`) — output in `[1, 20]`.
  - `loadParallelMaxResults` (`parallel_search.go:194`) — output in
    `[1, 10]`.
  - `loadParallelTimeout` (`parallel_search.go:180`) — output `> 0`.
  - `normalizePort` (`bot.go:157`) — output `"5000"` or a valid port
    string in `[1, 65535]`.
  - `loadAnalysisTimeout` / `loadAnalysisMaxOutputTokens`
    (`stock_analysis.go:218`, `:233`) — either a value `> 0` or a clean
    error; never panic.
  - `loadExplainRateLimiter` / `loadAnalysisRateLimiter`
    (`rate_limiter.go:87`, `bot.go:446`) — `limit > 0`, `window > 0`.
- **Property:** For arbitrary `hegel.Text()` input, the output is always
  in the documented range (or a clean error for the error-returning
  variants) and the function never panics.
- **Why:** These are small, pure functions with a clear contract and
  currently have only a handful of fixed examples each. Cheap coverage,
  good for the 50% CI gate.

#### 11. `extractFixedXLinks` invariants

- **Target:** `internal/bot/xlink.go:75`.
- **Properties:**
  - Output is deduplicated.
  - `len(output) <= maxXLinksPerMessage` (5).
  - Every entry starts with `https://fixupx.com/` or
    `https://fxtwitter.com/` and contains `/status/[0-9]+`.
  - Output is a subsequence of the rewritten input matches (order
    preserved).
- **Generator:** Build `text` by splicing arbitrary prose
  (`hegel.Text()`) around N generated tweet URLs. Generate tweet URLs
  with `hegel.FromRegex("https://(?:www\\.)?(?:x|twitter)\\.com/[a-z]+/status/[0-9]+",
  true)` and mix in non-tweet URLs and profile-only URLs to exercise the
  rejection paths.

### Review-pass additions

A second review pass surfaced these. Numbering continues from the list
above so existing entries keep their numbers; tier is noted inline.

#### 12. Output-safety PBT for `plainTelegramMarkdownText` — Tier 1

- **Target:** `internal/bot/telegram_markdown.go:115`.
- **Why:** Catches bug 4. The function's only test
  (`TestPlainTelegramMarkdownText`, `telegram_markdown_test.go:20`) uses a
  well-formed input and doesn't cover the no-NUL / valid-UTF-8 contract.
  `FuzzFormatAndNormalizeMarkdown` asserts that contract for
  `formatTelegramMarkdown` — the plain path's sibling — but never calls
  the plain path.
- **Properties:**
  - `utf8.ValidString(plainTelegramMarkdownText(s))` for all `s`.
  - `!strings.Contains(plainTelegramMarkdownText(s), "\x00")`.
  - Consistency: feed the same `s` to both formatters; both outputs are
    NUL-free and valid UTF-8.
- **Generator:** `hegel.Text()` plus inputs that splice in NUL and invalid
  UTF-8 bytes — draw `hegel.Binary(0, 50)` segments and interleave them
  with `hegel.Text()` so malformed sequences actually appear (full-Unicode
  `Text()` alone is always valid UTF-8 and won't exercise the bug).
- **Note:** the idempotence / NUL / UTF-8 / backslash-escape properties
  for `formatTelegramMarkdown` and `normalizeGeneratedTelegramMarkdown`
  are *already* covered by `FuzzFormatAndNormalizeMarkdown`
  (`fuzz_test.go:383-406`) — don't duplicate them.

#### 13. Placeholder-collision soundness for `formatTelegramMarkdown` — Tier 1

- **Target:** `internal/bot/telegram_markdown.go:22` (token scheme at
  lines 30-33, restore at lines 78-81).
- **Why:** The tokenizer replaces matched spans with sentinels
  `TGMARKTOKEN<n>X`, then restores them with `strings.ReplaceAll`. If the
  untrusted input itself contains that literal sentinel, the final
  `ReplaceAll` rewrites the user's bytes — a corruption / injection
  vector. Neither Go's byte-mutation fuzzer nor plain `hegel.Text()` will
  spontaneously emit the exact 12-char sentinel, so this needs inputs that
  deliberately embed it.
- **Property:** for `text := prefix + "TGMARKTOKEN0X" + "**bold**"` with
  `prefix := hegel.Text()`, the literal sentinel from `prefix` must not be
  replaced by token content — it should survive (escaped) into the output.
- **Generator:** `hegel.Text()` for surrounding prose; construct the
  sentinel-bearing input inline. Also try multiple sentinels with indices
  that do and don't correspond to real tokens (`TGMARKTOKEN5X` with only
  two tokens present).

#### 14. Rune-budget cascade PBT for `buildAnalysisPrompt` — Tier 1

- **Target:** `internal/bot/stock_analysis.go:443-463`.
- **Why:** `FuzzBuildAnalysisPrompt` (`fuzz_test.go:552`) only feeds short
  strings and small floats, so it **never enters the drop loop** — it
  checks nonce/marker/footer but never the budget contract or the cascade.
- **Properties:**
  - No-crash / always-marshals when droppable fields are huge — draw large
    `hegel.Lists(...)` for `NewsItems` and large `Metrics`.
  - Cascade terminates and is monotone: each iteration strictly shrinks
    `payloadJSON` until `<= maxPromptTotalRuneLen` (6000) or hits the
    `break` at line 456.
  - **Documents a real limitation:** the cascade only drops
    price-target/recommendation/earnings/metrics/news; a giant `Symbol` or
    `Profile.Name` is *not* droppable, so the prompt can exceed 6000 runes
    via the `break` path. A PBT that draws a large `profileName` and
    asserts the bound will fail — surfacing that the budget isn't enforced
    for non-droppable fields. Decide whether that's intended (and if so,
    truncate those fields before the cascade).
- **Generator:** draw `Symbol`/`profileName` from `hegel.Text()` with a
  large `MaxSize`, and `NewsItems` from `hegel.Lists(...)` with a
  separately-drawn size (default list sizes are too small to blow the
  budget).

#### 15. Length/bounds PBT for `toPromptWebResults` — Tier 2

- **Target:** `internal/bot/gemini_explainer.go:230`.
- **Properties:**
  - `len(toPromptWebResults(rs)) <= len(rs)` (structural).
  - No-crash on arbitrary `[]parallelSearchResult`.
- **Budget caveat:** `toPromptWebResults` only copies fields — per-field
  rune budgets are enforced by `sanitizeParallelResults`
  (`parallel_search.go:154`) upstream. A budget property must target
  `toPromptWebResults(sanitizeParallelResults(rs))`, not
  `toPromptWebResults(rs)` alone, or it will fail on inputs the function
  was never responsible for cleaning.
- **Generator:** build `[]parallelSearchResult` inline with `hegel.Text()`
  fields and `hegel.Lists(...)` for the slice. For the budget variant,
  pipe through `sanitizeParallelResults` first.

#### 16. Robustness PBT for `formatLeetCodeMessage` — Tier 2

- **Target:** `internal/bot/leetcode.go:121`.
- **Contract note:** The function dereferences `question` immediately at
  line 128 (`question.Difficulty`). Its contract requires a non-nil
  pointer; do **not** include nil in the PBT or it will report a
  contract violation, not a bug.
- **Properties:** For any non-nil `*LeetCodeQuestion` with arbitrary
  field values:
  - Never panics.
  - If it routes through Telegram markdown, output is NUL-free and valid
    UTF-8 (same safety contract as bug 4).
- **Generator:** build `LeetCodeQuestion` inline with `hegel.Text()`
  fields; draw `Difficulty` from `hegel.SampledFrom([]string{"Easy",
  "Medium", "Hard", ""})` plus `hegel.Text()` to exercise the
  unknown-difficulty path. Skip nil.

#### Considered and skipped

- **`stripAskPrefix`** (`ask.go:366`) — only weak invariants hold
  (`result == strings.TrimSpace(result)`); it is **not** idempotent
  (`"ask ask x"` → `"ask x"`) and the result can still be `"ask"`
  case-insensitively, so there's no strong falsifiable property worth a
  PBT. A unit test is a better fit.

### Porting the existing 16 `Fuzz*` tests

Not urgent — they already work — but Hegel gives better shrinking and a
persistent `.hegel/` example database that surfaces regressions on
re-run. Highest-value ports, in priority order:

1. **`FuzzPriceTargetUpsidePct`** → finds bug 1 immediately with Hegel's
   Inf-inclusive `Floats[float64]()`.
2. **`FuzzTruncateRunes` / `FuzzSanitizeForPrompt`** → Hegel's full-Unicode
   `Text()` reaches combining marks and `ß`→`SS` that Go's
   byte-mutation fuzzer rarely hits.
3. **`FuzzFormatAndNormalizeMarkdown`** → idempotence under real Unicode.
4. The remaining `Fuzz*` tests are lower priority; port them when the
   Hegel dependency is already in place.

See `references/go/porting.md` in the Hegel skill for the mechanical
translation. The main changes are `f.Fuzz(func(t *testing.T, ...))` →
`hegel.Test(t, func(ht *hegel.T) { ... })` and `f.Add(...)` seed cases →
inline `hegel.Draw` calls.

## Setup

Hegel is not in `go.mod` and `.hegel/` is not in `.gitignore`. To start:

```bash
go get hegel.dev/go/hegel@latest
echo ".hegel/" >> .gitignore
```

Hegel persists failing examples to `.hegel/` and replays them on
subsequent runs; in CI the database is auto-disabled. No other config is
required for `hegel.Test` to work.

## Suggested order of work

1. **Fix the four confirmed bugs.** They're small, isolated, and each has
   a one-line fix. Bug 4 (plain-text sanitization) is the cheapest — copy
   the two-line prelude from `formatTelegramMarkdown`.
2. **Add the rate-limiter stateful model test (opportunity #1).** This is
   the highest-value single test in the repo and would have caught bug 3.
3. **Add the float-finiteness PBT (opportunity #2).** Catches bug 1 and
   generalizes to every JSON-serialized float in the prompt payload.
4. **Add the mention-soundness PBT (opportunity #3).** Catches bug 2.
5. **Add the plain-text safety PBT (opportunity #12).** Catches bug 4 and
   pins the contract so the two markdown formatters can't drift again.
6. Port `FuzzPriceTargetUpsidePct` to Hegel (or just delete it once #2
   is in place — they overlap).
7. Work through the rest of Tier 1 (including the new opportunities #13
   and #14), then Tier 2 as coverage gaps appear.
