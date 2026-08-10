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
TIME      PROVIDER   MODEL             IN    OUT   COST      LATENCY
14:32:01  anthropic  claude-sonnet-5   1203  412   $0.0098   1840ms
14:31:40  openai     gpt-4o            890   210   $0.0044   920ms

2 calls, $0.0142 total
```

Traces are stored locally in `~/.agentotel/spans.db` (SQLite) — nothing
leaves your machine.

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
arrives — tracing adds no latency and doesn't break streaming, whether or
not the call ends up being parseable (see the streaming caveat below). In
parallel, up to 1MB of the body is captured; once it's fully read, agentotel
parses the `usage` field already present in the JSON (no extra request, no
token-counting guesswork), prices it against an embedded, PR-editable
pricing table (`internal/pricing/models.yaml`), and hands it to a background
writer that persists it to a local SQLite database (WAL mode, so `agentotel
start` and `agentotel trace` running at the same time don't collide) —
none of this is on the request's critical path.

## Status: v0.1

Supports OpenAI (`/v1/chat/completions`) and Anthropic (`/v1/messages`).

**Known gap**: streaming (`stream: true`) responses are forwarded correctly
(see above — no added latency) but aren't parsed into a span yet, since the
body arrives as SSE frames rather than one JSON object. Most real agents
default to streaming, so most calls aren't traced yet — tracked in
[#1](https://github.com/vigneshakaviki/agentotel/issues/1). Non-streaming
calls (e.g. Aider's own commit-message generation) are captured today.

Roadmap:

- **v0.5**: streaming (SSE) span capture (#1), Ollama + Gemini providers,
  OTLP export (`agentotel export --format otlp`) for Grafana/Jaeger,
  cost-budget alerts.
- **v1.0**: optional hosted trace storage for people who don't want to run
  their own Grafana.

Adding a new provider is two files: implement `providers.Parser` (see
`internal/providers/openai.go` for the shape) and add one entry to
`DefaultRoutes` in `internal/proxy/parser.go`. Updating pricing is a
one-line YAML edit in `internal/pricing/models.yaml` — no Go code required.

## License

MIT — see [LICENSE](LICENSE).
