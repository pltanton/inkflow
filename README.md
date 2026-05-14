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

`webdav_user` and `webdav_pass` can be set in TOML or through `INKFLOW_WEBDAV_USER` and `INKFLOW_WEBDAV_PASS`.

`state_file` defaults to `XDG_STATE_HOME/inkflow/state.db`, then `~/.local/state/inkflow/state.db`.

`template_dir`, if set, overrides the built-in templates in `internal/plan/templates`.

Minimal example:

```toml
vault_dir = "/home/anton/Obsidian"

[[route]]
from = "Syncs/"
pdf_dir = "_files/Attachments/Boox/Syncs"
note_dir = "02. Areas/Wallet/Syncs"
note_name = "{stem}.md"
pdf_name = "{stem}.pdf"
template = "sync"
```

## Run

```bash
go run ./cmd/inkflow --config inkflow.toml serve
```

```bash
go build ./cmd/inkflow
```

## NixOS

See [`nix/example.nix`](./nix/example.nix) and the `services.inkflow` module in [`nix/inkflow.nix`](./nix/inkflow.nix).
