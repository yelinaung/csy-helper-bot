# URL Extraction in the Ask Flow

Status: proposed
Date: 2026-07-30

## Overview

When a user asks the bot a question that points at a web page — either the
question itself contains a URL, or the user replies to a message containing
one — ground the answer in the actual page content using the
[Parallel Extract API](https://docs.parallel.ai/extract/extract-quickstart).
Extract converts any public URL (including JavaScript-rendered SPAs and PDFs)
into clean markdown excerpts focused on an objective.

No new command. This extends the existing `@bot` ask flow:

```text
@csy_bot https://example.com/pricing what are the plan prices?
→ bot extracts the page, Gemini answers from the excerpts

@csy_bot summarize this
  (as a reply to a message containing a link)
→ bot pulls the URL from the quoted message, extracts, summarizes

@csy_bot https://example.com/article
→ bare link: extracts with a generic "summarize the key points" objective
```

The flow already grounds fresh-info questions in Parallel *Search* results
(`answerTextQuestion` → `classifySearchNeed` → `explainWithSearchResults`).
Extraction adds a second retrieval source ahead of that same path and reuses
the same `PARALLEL_API_KEY`.

## API reference

| | |
|---|---|
| Endpoint | `POST https://api.parallel.ai/v1/extract` |
| Auth | `x-api-key: $PARALLEL_API_KEY` (same as Search) |
| Pricing | $0.001 per URL ($1 CPM) |
| Limits | up to 10 URLs per request; 600 req/min (beta) |

Request body:

```json
{
  "urls": ["https://www.un.org/en/about-us/history-of-the-un"],
  "objective": "When was the United Nations established?"
}
```

Response (relevant fields):

```json
{
  "extract_id": "...",
  "results": [
    {
      "url": "...",
      "title": "...",
      "publish_date": "YYYY-MM-DD",
      "excerpts": ["...markdown..."]
    }
  ],
  "errors": [],
  "usage": [...]
}
```

`full_content: true` returns whole-page markdown; v1 uses excerpts only.

## Design

### Where it hooks in: `answerTextQuestion` (ask.go)

Current flow:

```text
question/quoted → classifySearchNeed → [search → explainWithSearchResults]
                                     → explainWithLanguage (fallback)
```

New flow:

```text
question/quoted → detect URLs
  ├─ URLs found → extract ── ok ──→ explainWithExtractResults
  │                └─ retrieval failure / zero excerpts → fall through ↓
  └─ no URLs → classifySearchNeed → ... (unchanged)
```

URL detection runs **before** the freshness classifier: when the user
explicitly points at a page, there is nothing to classify (and skipping the
classifier saves a Gemini call).

**Scope boundary — replied photos win.** `answerAskQuestion` branches to
image analysis whenever the replied-to message has a photo
(`repliedPhoto != nil`), *before* `answerTextQuestion` runs. This hook lives
inside `answerTextQuestion`. A URL in a **photo's** caption never reaches
it — the ask is answered by image analysis, exactly as today. That
precedence is deliberate for v1; a combined image+page path is out of
scope.

The replied-caption URL case applies only to **non-photo media** — videos,
documents, audio. There, `extractRepliedPhoto` returns `nil`, and the text
path runs with `ReplyToMessage.Caption` as the quoted text.

The fallback covers **retrieval failures only** — URL detection coming up
empty, the extract call failing, or zero usable excerpts. That preserves the
"users never see a retrieval error" rule; worst case the answer is
ungrounded, exactly as it is today when search fails.

Once extraction has succeeded, errors from `explainWithExtractResults` —
`ErrExplainBlocked`, `ErrExplainTimeout`, and so on — propagate to
`askHandler` exactly as `explainWithSearchResults` errors do, surfaced
through `explainErrorToUserText`. They are never retried ungrounded: a
retry would discard a safety verdict and burn a second model call for
nothing.

### URL detection

New helper `extractQuestionURLs(message, question, quoted) (urls []string, strippedQuestion string)`:

1. **Prefer Telegram entities** — `url` and `text_link` entities are the
   authoritative source and catch bare domains like `example.com` that a
   scheme-prefix scan would miss. Two sources, matching exactly what the
   flow already sends to Gemini:

    - the user's own question: `message.Entities`, always;
    - the quoted side: **only the entity array belonging to the text source
    `extractQuotedText` selected**, following its precedence —
    `message.Quote.Entities` when a manually selected quote exists,
    otherwise `ReplyToMessage.Entities` when the reply has text, otherwise
    `ReplyToMessage.CaptionEntities` when it has a caption. When the user
    highlighted a specific snippet, `extractQuotedText` deliberately
    ignores the rest of the replied message; harvesting the full
    `ReplyToMessage` entity arrays anyway would extract (and bill) URLs
    the user explicitly quoted *around*.

    Hidden `text_link` targets live *only* in these arrays — the fallback
    text scan sees anchor text, not the URL — so the selected source's array
    must not be skipped. Reuse `utf16EntityRangeToByteRange` (already in
    ask.go) to slice entity text; for `text_link`, use `entity.URL`.
2. **Skip bot-owned quoted messages** — when `isQuotedFromBot(message)`
   (already in ask.go) is true, harvest no URLs from the quoted text. The
   bot's own answers carry citation links from the search path; a user
   replying "@bot explain this" to one of them should get an explanation of
   the *text*, not billed extractions of the bot's citations. URLs the user
   types in their own question still count.

    **Known limitation:** `isQuotedFromBot` checks
    `message.ReplyToMessage.From` and does **not** handle `message.Quote`
    (manually-selected quotes in the Telegram UI). `Quote` has no author
    field, so there is no way to determine that the selected text came from
    the bot. A user manually highlighting bot text and typing `@bot explain
    this` will still trigger extraction of URLs inside that quote. This is a
    Telegram API limitation; the Quote case is noted in Out of scope.
3. **Fallback text scan** — tokens starting `http://` or `https://` in the
   question and quoted text, for messages arriving without entities (some
   test paths and edge cases).
4. Normalize: prepend `https://` to scheme-less entity URLs; `url.Parse`
   must succeed, scheme must end up http/https, host non-empty; drop
   obviously local hosts (`localhost`, private IPs) and raw URLs over
   2048 chars. Skipped URLs are dropped silently (the question may still be
   answerable without them). **Known limitation:** an HTTP-only site reached
   via a scheme-less bare domain will fail extraction under `https://`; it
   surfaces in the response `Errors` and is skipped like any dead link. An
   `http://` retry for scheme-less URLs is out of scope for v1 — users can
   paste the explicit `http://` URL, which passes through unchanged.
5. De-duplicate; cap at `EXTRACT_MAX_URLS` (default 3 — each URL is billed
   and more context dilutes the answer).

Ordering rule: URLs from the question come before URLs from the quoted
message.

Objective rule: `strippedQuestion` is the question text with its URLs
removed. The **caller** builds the objective from it: if non-empty, it *is*
the objective; if empty, the objective defaults to "Summarize the key points
of this page". The empty case covers both a question that is only a URL
*and* a bare `@bot` mention replying to a link-bearing message
(`shouldHandleAskMention` accepts that ask with an empty question) — either
way Parallel must never receive an empty objective, a requirement
`search()` already enforces against blank objectives.

### New file: `internal/bot/parallel_extract.go`

Match `parallel_search.go`'s structure exactly:

- `parallelExtractor{baseURL, apiKey, timeout}` +
  `newParallelExtractor()` returning `nil` when `PARALLEL_API_KEY` is unset
  (feature silently off, following `newParallelSearcher()`'s own
  convention) **or when `EXTRACT_ENABLED` is explicitly false**. The
  constructor enforces the kill switch, not the call sites: parse with
  `strconv.ParseBool`; unset or unparsable values default to enabled, with
  a `log.Warn` on an unparsable value, following the lenient-loader pattern
  already used elsewhere in the package. A unit test covers the disabled
  path (`EXTRACT_ENABLED=false` + key set → `nil`) and the invalid-value
  path (`EXTRACT_ENABLED=banana` → enabled).
- Response structs — unlike `parallelSearchResponse`, the Extract response
  carries per-URL errors, and the struct **must** include them, or
  `encoding/json` silently discards them:

    ```go
    type parallelExtractResponse struct {
      ExtractID string                  `json:"extract_id"`
      Results   []parallelExtractResult `json:"results"`
      Errors    []parallelExtractError  `json:"errors"`
    }

    // Matches the v1 OpenAPI ExtractError schema
    // (https://docs.parallel.ai/api-reference/extract/extract).
    type parallelExtractError struct {
      URL            string `json:"url"`
      ErrorType      string `json:"error_type"`
      HTTPStatusCode *int   `json:"http_status_code"` // null when unavailable
      Content        string `json:"content"`          // JSON null decodes to ""
    }
    ```

    The response also carries optional `warnings` and `usage` arrays and a
    `session_id`; v1 ignores them. Test fixtures must use this documented
    shape — a made-up `message` field would decode to empty values in
    production and defeat the per-URL diagnostics this struct exists for.
- `extract(ctx, urls []string, objective string) ([]parallelExtractResult, error)`
  following `search()`: span, per-call context timeout, `x-api-key` header,
  drain-body-before-close, bounded error body (reuse
  `maxParallelErrorBodyBytes`), non-200 → error. Reuse `parallelHTTPClient`.
- `extract()` logs each entry in `Errors` (`log.Warn` with host,
  `error_type`, `http_status_code`, and bounded `content`) and skips it —
  not fatal, since one dead link shouldn't kill a two-link question. A
  step-1 unit test asserts a mixed success/error response still returns the
  successful results.
- `sanitizeParallelExtractResults()` follows `sanitizeParallelResults()`
  for per-field sanitization (`sanitizeForPrompt` on title and excerpts)
  but with a **stricter keep rule**: a result must have at least one
  non-empty excerpt after sanitization, or it is dropped — title alone is
  not enough. This deliberately diverges from the search sanitizer's
  "title OR excerpts" rule: search snippets feed a general answer, but the
  grounded explainer's entire premise is page *content*. A title-only
  result would count as a successful extraction, invoke
  `explainWithExtractResults` with no page text, and bypass the
  zero-excerpt fallback. If every result is dropped, the caller sees zero
  usable excerpts and falls through (see Error handling). Budgets: page
  excerpts are richer than search snippets, so allow ~1500 runes per
  excerpt, max 4 excerpts per URL. The overall prompt budget scales with
  the configured `EXTRACT_MAX_URLS`, not a hard-coded 3, so raising the
  knob keeps working (see Configuration).

### New explainer method: `explainWithExtractResults` (gemini_explainer.go)

Modeled on `explainWithSearchResults`:

- Prompt frames the excerpts as **untrusted page content** to answer from,
  with URL/title/publish date per source — same injection posture the search
  prompt already takes.
- Instructs Gemini to answer the user's question from the content, cite
  which page a claim comes from when multiple URLs are present, and say so
  when the excerpts don't contain the answer.
- Same Burmese handling: `respondInBurmese` flows through unchanged, so
  Burmese questions about a page get Burmese answers for free.

### What is reused for free (why no new command wins)

| Concern | Covered by |
|---|---|
| Rate limiting | existing `explainLimiter` — extraction only happens inside an already-allowed ask request |
| Group allowlist | existing middleware |
| "thinking..." + edit-in-place reply | `sendOrEditExplainResult` |
| Markdown escaping + plain-text fallback | `formatTelegramMarkdown` fallback chain |
| Burmese detection | `shouldRespondInBurmese` |
| Metrics/spans per command | existing `obs("bot.explain", ...)` wrapper |

### Configuration

No new required config; the feature activates when `PARALLEL_API_KEY` is
already set (same key as search). Optional knobs:

```text
# URL extraction inside @bot questions (optional — requires PARALLEL_API_KEY)
EXTRACT_ENABLED=true                 # default true when key present; kill switch
EXTRACT_TIMEOUT_SECONDS=30           # default 30 (page rendering is slower than search)
EXTRACT_MAX_URLS=3                   # default 3, clamped to 1..10 (API limit)
```

`EXTRACT_MAX_URLS` is a real knob, not documentation: it drives the URL
detection cap and the sanitizer/prompt budgets, following the
`loadParallelMaxResults` pattern (default on missing/invalid, clamp to the
API limit). A configured value must never be silently ignored.

### Telemetry

Follow the PII conventions in `parallel_search.go` (which records
`objective_len`, never raw text):

- Span `parallel.extract` with `parallel.urls_count`,
  `parallel.objective_len`, `parallel.excerpts_count` — counts and lengths
  only, the exact posture `parallel.search` takes today. No URL hosts in
  v1: full user-pasted URLs can carry PII in paths/query strings, and
  recording hosts for extract but not search would make the two spans
  inconsistent to debug. If host-level visibility turns out to be needed,
  add it to both spans in one change.
- The sanitizing span exporter needs no changes (Extract authenticates via
  header, not query param).
- Add a `bot.result`-style breadcrumb: log whether an ask request took the
  extract path, the search path, or the plain path (already partially there
  via the existing `log.Info` lines).

### Error handling

Retrieval failures are soft — the ask flow falls through and answers without
page context. Explainer failures after a successful extract are **not**
retried; they propagate like any other explain error:

| Failure | Behavior |
|---|---|
| URL invalid/local/too long | drop that URL silently; proceed with the rest |
| All URLs dropped | plain flow (classifier → search/plain), unchanged from today |
| Extract non-200 / timeout | `log.Warn`, fall through to plain flow |
| Some URLs in `Errors` | log + skip those, answer from the rest |
| Zero usable excerpts | fall through to plain flow |
| `explainWithExtractResults` fails (blocked, timeout, ...) | propagate to `askHandler` → `explainErrorToUserText`; no ungrounded retry |

Known tradeoff: on a bare-link question ("@bot <url>") the fallback answer
will be Gemini saying it can't open links. That failure mode already exists
today for pasted links, so extraction only ever improves the experience.
Never surface Parallel error bodies in chat (quota/account details); log
them bounded to 1KB as `search()` does.

Latency note: the extract failure + search-success fallback path triggers
four model/API calls (extract → Gemini classifier → search → Gemini explain)
— the worst-case latency of any ask path. This is acceptable for v1 because
the extract call is fast enough that the marginal cost over a plain search
ask is small, and the failure case is rare once the API is stable.

## Implementation steps

1. **`parallel_extract.go`** — client, config loaders, sanitizer.
   Unit tests with `httptest.Server`, modeled on `parallel_search_test.go`
   (success, per-URL errors, non-200, timeout, malformed JSON, budgets).
   The constructor `newParallelExtractor()` must check `EXTRACT_ENABLED`
   before returning a non-nil extractor — the `PARALLEL_API_KEY` check
   alone is not enough (the kill switch would silently have no effect if
   implementation follows the search pattern of checking only the key).
2. **`extractQuestionURLs`** — entity-based + text-scan URL detection with
   validation/dedup/cap. Table-driven tests plus a fuzz target in
   `fuzz_test.go` (must never panic on arbitrary text/entities; entity
   offsets are UTF-16 and hostile inputs are a known sharp edge — see the
   existing `utf16EntityRangeToByteRange` tests). Include a test case for
   entity arrays with non-zero offsets when the associated text source is
   empty (e.g., a quoted image caption with entities) — these are handled
   naturally by `utf16EntityRangeToByteRange` returning `(0, 0, false)`, but
   the case should still be covered.
3. **`explainWithExtractResults`** — prompt + method in
   `gemini_explainer.go`, tests alongside the `explainWithSearchResults`
   ones.
4. **Wire into `answerTextQuestion`** — URL branch ahead of the freshness
   classifier, fall-through on retrieval failures only. Extend the
   `explain_feature_test.go` handler tests: URL in question, URL in quoted
   reply, `text_link` inside a manually selected quote, URL *outside* the
   selected quote (ignored — only the selected source is harvested),
   `text_link` in a replied **non-photo** media caption (video/document —
   extraction runs), caption URL on an actual replied **photo** (image
   branch wins, no extraction), bare link, bare `@bot` reply to a link
   (objective defaults), mixed question+link, quoted message from the bot
   itself (its URLs ignored), title-only extract result (dropped → falls
   through), extract API down (falls back), explainer blocked after
   successful extract (error surfaces, no ungrounded retry), Burmese
   question with link.
5. **Docs** — README (`@bot` command description + env-var block).

Out of scope for v1: photo-ask flow (`photoAskHandler`) with links in
captions, a combined image+page path when a replied photo's caption contains
a URL (image analysis wins — see the scope boundary in "Where it hooks in"),
`full_content` mode, an `http://` fallback retry for scheme-less bare
domains that fail under `https://`, and filtering citation-style URLs out of
manually-selected `Quote.Entities` from the bot's own messages (the `Quote`
struct carries no author field — see URL detection step 2).

## Resolved questions

1. **Quoted-message URLs trigger extraction** even when the question doesn't
   mention the link — replying to a message and asking is an explicit signal
   the link is the subject. Exception: quoted messages from the bot itself
   are never harvested (see URL detection step 2).
2. **Bare-domain entities are extracted** (`example.com` without scheme):
   Telegram only tags things it renders as links, and entities make
   detection reliable. Normalized to `https://`; the HTTP-only limitation is
   documented in URL detection step 4.
