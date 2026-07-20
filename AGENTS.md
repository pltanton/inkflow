# AGENTS.md — Inkflow

WebDAV bridge: accepts PDF uploads from BOOX e-ink devices, stores in an Obsidian vault, generates Markdown notes from templates. Optional Gemini 2.5 Flash AI for OCR + summaries.

Single Go package (not a monorepo). No codegen, no build framework.

---

## Commands

```bash
go build ./cmd/inkflow          # binary: ./inkflow
go run ./cmd/inkflow --config inkflow.toml serve
go test ./...
go test -v ./internal/plan
go test -run TestSelectMostSpecificMatchWins ./internal/plan
go test -race ./...
```

Verbose logging: `SLOG_LEVEL=debug go run ./cmd/inkflow --config inkflow.toml serve`

---

## Config (TOML)

Required: `vault_dir`. Every route must have `from`. Missing either causes startup error.

Filename pattern placeholders: `{date}` (YYYY-MM-DD), `{stem}` (basename without ext), `{slug}` (URL-safe title), `{ext}`.

PDF filename parsing: first 10 chars used as date if YYYY-MM-DD; `[bracket text]` becomes tags (slugified); remainder becomes title.

Example: `2026-05-06 Meeting [finance].pdf` → date `2026-05-06`, title `Meeting`, tags `["finance"]`.

Template lookup order (first match wins):
1. `{template_dir}/{name}.md.tmpl`
2. `{template_dir}/default.md.tmpl`
3. Embedded `templates/{name}.md.tmpl`
4. Embedded `templates/default.md.tmpl`

No fallback to default on template render error — import fails.

---

## Environment Variables

| Var | Purpose |
|-----|---------|
| `GEMINI_API_KEY` | API key; takes precedence over `api_key_file`; empty string falls back to file |
| `WEBDAV_USER` / `WEBDAV_PASS` | Auth fallback if not in config |
| `XDG_STATE_HOME` | Base for default state file path |
| `SLOG_LEVEL` | Log level (`debug`, `info`, etc.) |

If any route has `ai = true` and no API key is available at startup, server refuses to start.

---

## Architecture

```
cmd/inkflow/main.go          CLI, Cobra, runtime wiring
internal/importer/importer.go  Main import logic (no tests — add carefully)
internal/webdavserver/server.go  HTTP/WebDAV, auth, request dispatch
internal/plan/                 Route matching, filename rendering, template render
internal/config/               TOML parsing + validation
internal/ai/gemini/client.go   Gemini API (base64 PDF → JSON ocr_text + summary_bullets)
internal/frontmatter/          YAML frontmatter tag manipulation
internal/note/block.go         Marker block insert/replace
internal/state/store.go        BoltDB deduplication records
```

Data flow: `PUT /path/file.pdf` → route match → raw and stable PDF hash dedup check → template render → optional AI → write PDF + note.

`ai.Provider` interface (`internal/ai/provider.go`) has one implementation: `gemini.Client`.

---

## Non-Obvious Behaviors

**Deduplication skips AI:** Same SHA256 + same vault paths → import skipped entirely, no AI call. For the same WebDAV source and vault paths, a stable PDF hash also skips re-import when only volatile export metadata (dates or trailer ID) changed. Changing route (different pdf_dir/note_dir) bypasses dedup. Existing state records gain the stable hash on their next upload; a metadata-only change may therefore re-import once during migration.

**Marker blocks replaced, not appended:** On re-upload, `<!-- inkflow:ocr:start -->` and `<!-- inkflow:summary:start -->` blocks are fully replaced. Previous content lost.

**No Gemini retry:** On API failure, error message is written into the marker blocks and import continues. No retry logic exists.

**Route matching:** Longest prefix wins. Two routes with identical `from` → error at startup, not at request time.

**BoltDB exclusive access:** State file (`~/.local/state/inkflow/state.db` or `$XDG_STATE_HOME/inkflow/state.db`) is mode `0o600`. Concurrent inkflow processes will fail.

**fsnotify** is a direct dependency in go.mod but appears unused in the codebase.

---

## Testing

Packages with tests: `cmd/inkflow`, `internal/ai/gemini`, `internal/config`, `internal/frontmatter`, `internal/note`, `internal/plan`, `internal/webdavserver`.

No tests in: `internal/importer`, `internal/log`, `internal/state`, `internal/util`.

Patterns: `t.TempDir()` for file I/O, `t.Setenv()` for env vars, simple mock structs (no framework). See `mockProvider` in `internal/webdavserver/server_test.go` for the AI mock pattern.

---

## Nix

```bash
nix develop    # dev shell with Go 1.23
nix build      # build package
```

`nix/package.nix` uses `buildGoModule` with a `vendorHash` — update the hash when changing go.mod/go.sum.

NixOS module: `nix/inkflow.nix`. Example: `nix/example.nix`.
