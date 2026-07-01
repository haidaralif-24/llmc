# llmc — Design & Plan (v1 / Bare Minimum)

A minimal CLI/TUI for calling AI model APIs with BYOK (bring your own key),
multi-provider support, and lightweight session memory. Built to learn API
integration, CLI/TUI design, and Go — not to compete with existing tools.

---

## 1. Goals

- One tool, many providers (OpenAI-style, Anthropic, local/Ollama, etc.)
- BYOK — user supplies their own API keys, nothing hardcoded
- Simple persistent chat sessions (flat file storage, no DB server)
- opencode-style TUI: slash commands open a floating picker overlay
- Streaming responses that can be cleanly interrupted
- No over-engineering — every feature below is justified by actual use

## 2. Explicit Non-Goals (v1)

Do **not** build these yet, even if tempted:

- Plugin/extension system
- RAG / embeddings / vector search
- Multi-user / auth / teams
- GUI (desktop or web)
- Custom tokenizer / exact token counting (use provider-reported usage)
- Full LaTeX/image math rendering (Unicode approximation only)
- `/compare` side-by-side mode (nice-to-have, post-v1)

## 3. Tech Stack

- **Language:** Go
- **TUI framework:** `bubbletea` (event loop) + `bubbles` (fuzzy-filterable
  list component for pickers) + `glamour` (markdown rendering)
- **HTTP:** Go stdlib `net/http` (streaming via SSE / chunked reads)
- **Storage:** flat JSON files on disk (no embedded DB needed at this scale)

Rationale: bubbletea's `bubbles/list` gives fuzzy filtering out of the box,
matching the opencode-style picker with minimal custom code. Go also
cross-compiles to a single binary, which suits a CLI tool.

## 4. Core Architecture

```
llmc/
├── cmd/                 # entrypoint, flag parsing
├── internal/
│   ├── provider/        # provider adapters, one per WIRE FORMAT (not per company)
│   │   ├── provider.go  # common interface
│   │   ├── openai.go    # generic OpenAI-compatible client (base URL is a param)
│   │   └── anthropic.go # Anthropic's own message format (genuine outlier)
│   ├── session/         # session store (load/save/list JSON files)
│   ├── config/          # config file (endpoints, default model, key refs)
│   └── tui/             # bubbletea model, update, view; picker overlay
├── go.mod
└── README.md
```

### Provider interface (the one abstraction worth doing carefully)

```go
type Message struct {
    Role    string // "user" | "assistant" | "system"
    Content string
}

type Provider interface {
    Name() string
    Stream(ctx context.Context, model string, messages []Message) (<-chan Token, error)
}

type Token struct {
    Text string
    Done bool
    Err  error
}
```

Every provider adapter normalizes its own request/response shape into this.
Everything else in the app (TUI, session store) only ever talks to this
interface — it doesn't know or care which provider is active.

### Adapter types, not one file per company

Most providers (OpenAI, OpenRouter, Groq, Together, most self-hosted and
local servers, and Ollama's `/v1` endpoint) speak the **same
OpenAI-compatible chat completions format** — only the base URL and key
differ. Writing a new `.go` file per company duplicates `openai.go` for no
reason. Anthropic is the real exception with its own wire format.

So the provider package scales by **adapter type** (currently 2), not by
**provider count** (unbounded, grows every month):

```go
type OpenAICompatible struct {
    BaseURL string
    APIKey  string
    Name    string // display label only: "openai", "openrouter", "groq", etc.
}
```

New providers like OpenRouter, Groq, or a custom self-hosted endpoint are
**config entries**, not new code. See section 5.

Ollama is the same story — it exposes an OpenAI-compatible `/v1` endpoint,
so it's just another `openai_compatible` config entry, no special code.

## 5. Config

`~/.config/llmc/config.toml`

```toml
default_provider = "anthropic"
default_model    = "claude-sonnet-4-6"

[providers.anthropic]
type        = "anthropic"
endpoint    = "https://api.anthropic.com/v1/messages"
api_key_env = "ANTHROPIC_API_KEY"

[providers.openai]
type        = "openai_compatible"
endpoint    = "https://api.openai.com/v1/chat/completions"
api_key_env = "OPENAI_API_KEY"

[providers.openrouter]
type        = "openai_compatible"
endpoint    = "https://openrouter.ai/api/v1/chat/completions"
api_key_env = "OPENROUTER_API_KEY"

[providers.ollama]
type        = "openai_compatible"
endpoint    = "http://localhost:11434/v1/chat/completions"
api_key_env = "OLLAMA_API_KEY"   # usually unused/blank for local

[providers.my-custom-server]
type        = "openai_compatible"
endpoint    = "http://localhost:8080/v1/chat/completions"
api_key_env = "LOCAL_KEY"
```

- Keys are never stored in the config file directly — only the **name of
  the env var** that holds them. Keeps BYOK simple and avoids plaintext
  secrets in a dotfile.
- `type` selects which adapter (`openai_compatible` or `anthropic`)
  constructs the provider — a small factory in `config/` maps `type` →
  adapter constructor. Adding a new OpenAI-compatible provider (OpenRouter,
  Groq, a self-hosted server) is a **config edit, not new code**.
- `/endpoint` and `/model` pickers read their candidate lists from this file
  plus recent-use history (see below). The picker's freeform-entry fallback
  is also how ad-hoc/custom endpoints get added without pre-editing config —
  on confirm, offer to persist the new entry into `config.toml`.

## 6. Sessions (Memory)

`~/.local/share/llmc/sessions/<name>.json`

```json
{
  "name": "default",
  "provider": "anthropic",
  "model": "claude-sonnet-4-6",
  "system": null,
  "messages": [
    { "role": "user", "content": "explain quicksort" },
    { "role": "assistant", "content": "..." }
  ]
}
```

- v1 memory = whole message list, no summarization or windowing yet.
- Switching `/model` mid-session keeps history; only the active model
  pointer changes.
- Recently used models/providers logged to a small separate file
  (`~/.local/share/llmc/history.json`) to power the picker's "recent" tier.

## 7. TUI Behavior

### Layout
Single chat view: scrollable transcript + input box at the bottom. Markdown
rendered via `glamour`. Simple math (`1/2`, `x^2`, etc.) passed through a
small Unicode-substitution pass before rendering — no LaTeX engine.

### Slash commands (v1 set only)

| Command      | Behavior                                              |
|--------------|--------------------------------------------------------|
| `/model`     | Opens picker: recent + known + freeform entry          |
| `/provider`  | Same picker pattern, sourced from config providers      |
| `/session`   | Switch/create a session                                 |
| `/system`    | Set/replace system prompt for current session            |
| `/clear`     | Clear working context (not the saved session file)       |
| `/help`      | List commands                                            |
| `/quit`      | Exit                                                     |

One generic **picker overlay component**, parameterized by a list-provider
function — not a separate widget per command.

### Streaming & interrupts

- Only `Esc` and `Ctrl+C` stop an in-flight stream (Ctrl+S avoided — it's
  legacy XOFF and unreliable across terminals).
- While streaming:
  - New chat messages are **queued** (FIFO) and sent after the stream ends.
  - `/model` and `/provider` selections are **staged**, not applied
    immediately. A second selection of the same kind **replaces** the
    pending one (last selection wins) — no queue for these, just one
    pending value per kind.
  - On stream completion: apply pending model/provider (if any), then
    flush the message queue in order.

```go
type model struct {
    streaming       bool
    cancelFunc      context.CancelFunc
    pendingModel    *string
    pendingProvider *string
    messageQueue    []string
}
```

## 8. CLI Commands (non-TUI, scriptable)

For piping / one-shot use outside the TUI:

```
llmc ask "explain this" [--model X --provider Y]
cat file.py | llmc ask "explain this"
llmc sessions list
llmc sessions show <name>
llmc config set-key <provider>
```

## 9. Build Order (suggested milestones)

1. Provider interface + one working adapter (e.g. Anthropic) with plain
   non-streaming request — prove BYOK + config loading works.
2. Add streaming to that adapter, print tokens to stdout (no TUI yet).
3. Add a second provider adapter — validates the abstraction actually
   generalizes.
4. Session store: save/load JSON, basic `sessions list/show`.
5. Bare TUI: chat view + input box, no slash commands yet, streaming with
   Esc/Ctrl+C interrupt.
6. Slash command routing (`/` detection → dispatch table).
7. Generic picker overlay component, wire up `/model` and `/provider`.
8. Queueing behavior for messages + pending model/provider during streams.
9. Markdown rendering (glamour) + Unicode math substitution pass.

Everything past step 9 (compare mode, fallback routing, cost readouts,
autocomplete polish) is post-v1.

## 10. Post-v1 Ideas (not scoped, just noted)

- `/compare <model_a> <model_b>` — same prompt to two models side by side
- Provider fallback/retry chain on error or rate limit
- Usage/cost readout after each response (from provider-reported usage)
- Autocomplete polish in the picker overlay
- Context windowing/summarization for long sessions
