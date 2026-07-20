# inkflow

WebDAV bridge to Obsidian.

BOOX uploads a PDF to inkflow over WebDAV. Inkflow stores the PDF in your vault and creates or updates a Markdown note from a template.

## Flow

1. On BOOX, you create the note and fill date/tags with the built-in shortcuts.
2. BOOX sends the PDF to inkflow.
3. Inkflow writes the PDF into the vault and renders the note.
4. Obsidian sees the file in place.

![BOOX create screen](./assets/boox-1.png)

BOOX note creation with date and tag shortcuts on the built-in keyboard.

![BOOX note](./assets/boox-2.png)

The note on BOOX before upload.

![Obsidian vault result](./assets/obsidian.png)

The resulting file as it appears in Obsidian.

## Config

`vault_dir` is required. Add one or more `[[route]]` blocks to match incoming BOOX paths.

`listen_addr` defaults to `127.0.0.1:8080`.

`webdav_user` and `webdav_pass` can be set in TOML or through `WEBDAV_USER` and `WEBDAV_PASS` env vars.

`state_file` defaults to `XDG_STATE_HOME/inkflow/state.db`, then `~/.local/state/inkflow/state.db`.

`template_dir`, if set, overrides the built-in templates in `internal/plan/templates`.

### Health and metrics

`GET /healthz` is unauthenticated and returns 200 only when the state store is
readable. Prometheus metrics are disabled by default. Enable them with
`[observability] metrics_enabled = true`; without `metrics_addr`, `/metrics`
uses the normal WebDAV listener and its basic authentication. Setting
`metrics_addr` starts a dedicated, unauthenticated metrics-only listener, so it
MUST be bound to a private address or otherwise protected by network policy.

### AI OCR + Summary

When a route has `ai = true`, inkflow sends the whole PDF to the selected AI provider in a single call (inline; no client-side rasterization). Gemini and OpenAI are supported. Select one with `[ai].provider`, which defaults to `"gemini"`.

- `## Summary` — short action-item bullets.
- `## OCR` — faithful transcription of the handwritten content.

**API key.** Provide the selected provider's key via `GEMINI_API_KEY` or `OPENAI_API_KEY`, or set `api_key_file` in its `[gemini]` or `[openai]` block to a path inkflow reads at startup. The env var takes precedence.

**OpenAI billing.** ChatGPT Plus, Pro, and Codex subscription allowances **cannot** be used instead of API billing. Inkflow requires a standalone, separately billed `OPENAI_API_KEY` with pay-as-you-go OpenAI API access, exactly like it requires `GEMINI_API_KEY` today. There is no supported session, cookie, or subscription-based alternative, and none is planned: ChatGPT/Codex and the OpenAI API are separate products with no billing bridge.

**Cost and quality.** These prices are approximate and subject to change; actual cost varies with page count and content density.

| Model | Input $/1M tokens | Output $/1M tokens | Notes |
|---|---:|---:|---|
| Gemini 2.5 Flash | $0.30 | $2.50 | Native PDF support, cheapest well-rounded default |
| Gemini 2.5 Flash-Lite | $0.10 | $0.40 | Cheapest; verify handwriting accuracy before relying on it |
| OpenAI GPT-4.1 mini | ~$0.40 | ~$1.60 | Economical OpenAI option |
| OpenAI GPT-4.1 | ~$2 | ~$8 | Higher quality/accuracy OpenAI option |

**Privacy.** The paid Gemini API tier does not use your data for model training.

Example config with AI enabled on one route:

```toml
vault_dir = "/home/anton/Obsidian"

[ai]
provider = "gemini" # "gemini" (default) or "openai"

[gemini]
# Reads $GEMINI_API_KEY; falls back to api_key_file if the env var is empty.
api_key_file = "/run/secrets/gemini-api-key"
model = "gemini-3.5-flash"
timeout = "60s"
ocr_prompt = "Transcribe the handwritten page as clean readable Markdown. The goal is a document that reads well, not a pixel-accurate copy of paper layout. Join visually wrapped lines that belong to one sentence into a single flowing line. Do not preserve every line break from the paper. When the writer puts a single name or short phrase above a related cluster of items, render that header as a Markdown heading: `### Name`. Render dash, bullet, or arrow markers on the page as `-` list items. Use a blank line only between structural sections, not after every visual line wrap. Preserve visual markup: wrap text highlighted with a marker pen in `==text==`; wrap text inside a hand-drawn frame or box in `**text**` as a single bold span even if it wrapped across multiple lines; render hand-drawn checkboxes as `- [ ]` (empty) or `- [x]` (ticked). Keep the source language. Faithful transcription only — no translation, no summarization."
summary_prompt = "Summarize as 3-5 short bullets covering action items, decisions, deadlines, people. Use the source language. Plain bullets only — do not produce `[ ]` or `[x]` checkboxes. The reader maintains a separate TODO section elsewhere in the note."

[openai]
# Used when provider = "openai". Reads $OPENAI_API_KEY; falls back to api_key_file.
api_key_file = "/run/secrets/openai-api-key"
model = "gpt-4.1"
timeout = "60s"
ocr_prompt = "Transcribe the handwritten page as clean readable Markdown."
summary_prompt = "Summarize as 3-5 short bullets covering action items, decisions, deadlines, people."

[[route]]
from = "Syncs/"
pdf_dir = "_files/Attachments/Boox/Syncs"
note_dir = "02. Areas/Wallet/Syncs"
note_name = "{stem}.md"
pdf_name = "{stem}.pdf"
template = "sync"
ai = true
```

Routes without `ai = true` skip the AI call entirely.

## Run

```bash
go run ./cmd/inkflow --config inkflow.toml serve
```

```bash
go build ./cmd/inkflow
```

## NixOS

See [`nix/example.nix`](./nix/example.nix) and the `services.inkflow` module in [`nix/inkflow.nix`](./nix/inkflow.nix).
