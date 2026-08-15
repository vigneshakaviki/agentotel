# agentotel

Trace any AI agent's LLM API calls — cost, tokens, latency — with **zero SDK
integration**. Point your agent's OpenAI/Anthropic base URL at a local proxy
instead of the real API, and every call is transparently recorded and
forwarded on unmodified.

Every agent framework (LangChain, CrewAI, opencode, Aider, a raw script)
needs its own bespoke observability SDK today. agentotel intercepts at the
network layer instead, so it works with anything that makes an HTTP call to
a known LLM API — no code changes, no framework-specific integration to
write or maintain.

## Why a proxy, and not a log reader

The common alternative is reading the usage logs an agent writes to disk.
That is easier to set up, and it inherits whatever the agent chose to
record — which is often wrong. Tools built on Claude Code's JSONL
transcripts report input-token counts that are off by two orders of
magnitude, because most entries record `0` or `1` rather than the real
prompt size, and they miss thinking tokens entirely on reasoning models.
Some agents (Aider) write no structured usage ledger at all, so there is
nothing to read.

A proxy sees the provider's own `usage` object — the same numbers you get
billed against. That is the entire reason this exists: **the numbers are
the provider's, not the agent's self-report.**

The honest trade-off is that a proxy sits in the request path, so if it
dies your calls fail, whereas an SDK that dies only costs you telemetry.
agentotel narrows that gap rather than dismissing it:

- Tracing is wrapped in a recover — a bug in parsing or pricing can never
  fail the call it is observing. There is a test for exactly this.
- `GET /healthz` makes liveness externally checkable, so it can run under a
  supervisor that restarts it.
- It is local. No network hop, no vendor to have an outage, no egress.

## Quickstart

```sh
go build -o agentotel ./cmd/agentotel
./agentotel start
```

Then point your agent at the proxy instead of the real API:

```sh
export OPENAI_BASE_URL=http://localhost:8787/openai
export ANTHROPIC_BASE_URL=http://localhost:8787/anthropic
```

Run your agent as normal. Every call is forwarded to the real provider and
recorded locally. Check what it's been doing:

```sh
./agentotel trace --last 1h
```

```
TIME      AGENT       PROVIDER   MODEL             IN    CACHED  OUT   COST      LATENCY
14:32:01  claude-cli  anthropic  claude-sonnet-5   1203  18400   412   $0.0098   1840ms
14:31:40  aider       openai     gpt-4o            890   0       210   $0.0044   920ms

2 calls, $0.0142 total
```

Or summarize spend instead of listing calls:

```sh
./agentotel trace --last 24h --by agent
```

```
AGENT       CALLS  IN      CACHED   OUT    COST      AVG LATENCY
claude-cli  312    41203   1840233  22119  $4.8812   1620ms
aider       47     88190   0         9204   $0.9931   880ms

359 calls across 2 agent(s), $5.8743 total
```

`--by` also accepts `model`, `provider`, and `session`.

Traces are stored locally in `~/.agentotel/spans.db` (SQLite) — nothing
leaves your machine.

### Attribution

`AGENT` is derived automatically from the caller's `User-Agent`, so spend
splits per tool with no configuration. For finer grouping — one agent run,
one task — send a session header, which is the only optional client-side
change agentotel asks for:

```sh
curl -H "X-Agentotel-Session: refactor-auth" ...
```

### What is never recorded

Spans hold token counts, cost, latency, model, and attribution. **No
prompts, no completions, no messages.** The proxy sees your conversations
but the database cannot contain them, so a `spans.db` is safe to copy
around and there is no redaction step to get wrong. This is a schema-level
guarantee, not a setting.

### Manual smoke test

```sh
curl http://localhost:8787/openai/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

## How it works

```
agent / SDK  →  agentotel (local proxy)  →  real provider API
                       │
                       └─ tees the response, prices it, writes a span async
```

agentotel is a reverse proxy (`net/http/httputil.ReverseProxy`) in front of
each provider's real API. The response body is streamed to the caller as it
arrives — tracing adds no latency and doesn't break streaming. In parallel,
up to 1MB of the body is captured; once it's fully read, agentotel parses
the `usage` field already present in the JSON (no extra request, no
token-counting guesswork), prices it against an embedded, PR-editable
pricing table (`internal/pricing/models.yaml`), and hands it to a background
writer that persists it to a local SQLite database (WAL mode, so `agentotel
start` and `agentotel trace` running at the same time don't collide) — none
of this is on the request's critical path.

### Cached tokens are priced separately

Coding agents re-send a large cached prefix every turn, so for a real
session most input tokens are cache reads — billed at roughly a tenth of
the input rate, while cache *writes* cost a premium. Counting them as
ordinary input, or ignoring them, gets the cost badly wrong in the exact
workload this tool is for.

The two providers disagree on the accounting, and agentotel normalizes both:

| | Anthropic | OpenAI |
|---|---|---|
| cache fields | `cache_read_input_tokens`, `cache_creation_input_tokens` | `prompt_tokens_details.cached_tokens` |
| relationship to input count | **additive** — `input_tokens` excludes them | **subset** — already inside `prompt_tokens` |
| cache write charge | yes, 1.25× input | none (caching is automatic) |

After normalization the buckets are disjoint, so `Input`, `CacheRead`,
`CacheWrite`, and `Output` are each priced once. Rates live in
`models.yaml`; an unspecified cache rate falls back to the full input rate,
never to zero.

### Overhead

`go test ./internal/proxy -bench 'Direct|ThroughProxy'` measures the same
call with and without agentotel in the path. On an M-series laptop against
a local upstream:

```
BenchmarkDirect-10          33µs/op
BenchmarkThroughProxy-10    77µs/op
```

≈44µs added per request — against provider calls that take hundreds of
milliseconds, that is noise. Span capture happens after the response has
been streamed to the client, and the SQLite write is handed to a background
goroutine that drops rather than blocks if it falls behind, so tracing
cannot push back on the request path by construction.

## Status: v0.1

Supports OpenAI (`/v1/chat/completions`) and Anthropic (`/v1/messages`),
streaming and non-streaming.

Streaming (`stream: true`) responses are parsed from their SSE frames:

- **Anthropic** always includes usage in its stream (split across the
  `message_start` and `message_delta` events), so streaming calls are
  traced automatically.
- **OpenAI** only includes usage in its stream if the request sets
  `"stream_options": {"include_usage": true}` — without it, no chunk
  carries token counts and the call is logged but not traced.

Anthropic folds thinking tokens into `output_tokens` without breaking them
out, so reasoning tokens are reported for OpenAI only.

Roadmap:

- **v0.5**: Ollama + Gemini providers, cost-budget alerts, and OTLP export
  (`agentotel export --format otlp`) for Grafana/Jaeger. OTLP export will
  follow OpenTelemetry's GenAI semantic conventions, which model an agent
  run as `invoke_agent` → `chat` → `execute_tool`; note those conventions
  are still marked Development upstream, so the export will pin a version
  rather than track `main`.
- **v1.0**: optional hosted trace storage for people who don't want to run
  their own Grafana.

Adding a new provider is two files: implement `providers.Parser` (see
`internal/providers/openai.go` for the shape — `Parse` for a single JSON
response, `ParseSSE` for a streamed one, and a `normalize` step that maps
the provider's cache accounting onto the shared buckets) and add one entry
to `DefaultRoutes` in `internal/proxy/parser.go`. Updating pricing is a
YAML edit in `internal/pricing/models.yaml` — no Go code required.

## License

MIT — see [LICENSE](LICENSE).
