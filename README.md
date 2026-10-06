# vault

Your encrypted notebook for URLs, passwords, SSH logins, build commands and
runbooks. Works in the terminal and as a GUI, on Windows, macOS and Linux, and
syncs between your laptops through a private GitHub repo. Free.

```
vault acme                    → everything under acme/
vault acme.prod-db.password   → copied (clipboard clears after 30 s)
vault globex-build-console        → prints the command and copies it
vault prod db pass                → fuzzy: every word must match
vault                             → interactive search
vault gui                         → the GUI in your browser
```

## How it keeps your data private

- Everything is in **one encrypted file** (`vault.enc`): AES-256-GCM with a key
  derived from your master password (PBKDF2-SHA256, 600,000 rounds).
- Only that encrypted file is written to disk or pushed to GitHub. Without your
  master password it's useless to anyone, GitHub included.
- The master password is never stored. **If you forget it, the data is gone.**
- The GUI is served by `vault` itself on `127.0.0.1` only, behind a random
  per-session token, and auto-locks after 30 minutes without use.
- Copied values clear from the clipboard after 30 seconds (if you haven't
  copied something else since).
- `vault edit` / `vault add` use a private temp file that is wiped right after.

## Install or update (each laptop)

One command; run the same command again later to update.

**Linux / macOS**
```
curl -fsSL https://github.com/shelo16/vault/releases/latest/download/install.sh | sh
```
Installs to `~/.local/bin` (Linux) or `/usr/local/bin` (macOS, may ask for your
login password). On Linux also install a clipboard tool:
`sudo apt install xclip wl-clipboard`.

**Windows (PowerShell)**
```
irm https://github.com/shelo16/vault/releases/latest/download/install.ps1 | iex
```
Installs to `%LOCALAPPDATA%\Programs\vault`, adds it to your PATH and creates a
**Vault** Start-menu entry that opens the GUI. Open a new terminal afterwards.

Also needed: `git`. Manual install: download your file from the
[latest release](https://github.com/shelo16/vault/releases/latest)
(`vault-linux-amd64`, `vault-macos-arm64` for Apple Silicon, `vault-macos-amd64`
for Intel Macs, `vault.exe` + `vaultw.exe` for Windows) and put it on your PATH as `vault`.

## Set up sync (once)

1. On GitHub create a **private**, empty repo, e.g. `vault-data`.
2. On the first laptop:
   ```
   vault init git@github.com:<you>/vault-data.git
   ```
   Choose a master password (a few random words is good, e.g.
   `copper-lantern-river-seven`).
3. On each other laptop, the same command joins the existing vault:
   ```
   vault init git@github.com:<you>/vault-data.git
   ```
   Enter the same master password.

Each laptop needs git access to that repo — the SSH key or `gh auth login`
you already use for GitHub works. For tighter control, give each laptop its own
**deploy key** with write access to just this repo (repo → Settings → Deploy
keys); a lost laptop can then be revoked on its own.

**How sync works:** every edit is saved, committed and pushed automatically.
`vault sync` (or the Sync button) pulls the latest. Changes are merged **per
entry** — editing different entries on two laptops never loses anything; if the
same entry was edited on both, the newer edit wins. Deletions sync too. Offline?
Edits are saved locally and go up on the next sync.

## Add your data

Fastest: put it in a text file and import it (see `examples/sample.txt`):

```
[acme/prod-db]
username = APPUSER
password = ...
url = jdbc:sqlserver://...

[globex/deploy-prod]
note = """
step 1
step 2
"""
```

```
vault import my-data.txt      # then delete my-data.txt
```

Keys containing `pass`, `secret`, `token`, `apikey`, `private` are hidden
automatically; put `!` before any other key to hide it (`!pin = 1234`).

Day to day:

```
vault add globex/new-server           # opens your editor with a template
vault set globex/api-server/password # prompts without echo
vault set globex/build-console/cmd "mvn clean install ..."
vault edit globex                     # edit a whole group as text
vault mv globex/card globex/card-prod
vault rm globex/misc
```

Or just use the GUI (**+ New**, pencil icon to edit).

## Looking things up

| You type                          | You get                                         |
|-----------------------------------|-------------------------------------------------|
| `vault acme`                  | numbered list of acme entries; type a number |
| `vault acme/prod-db`          | the entry's fields, numbered; type a number to copy |
| `vault acme.prod-db.password` | copied                                          |
| `vault globex-build-console`      | single-field entry → printed and copied         |
| `vault api ssh`                  | fuzzy match on entry/field names                |
| `vault find 10.0.0.20`         | searches values too (not secrets)               |
| `vault -p globex/api-server/ssh` | prints the raw value (for scripts), no copy     |
| `vault -s acme/prod-db`       | shows secrets instead of `********`             |

Separators `/`, `.`, `-` and spaces are interchangeable in fuzzy matches.

**Interactive mode** — just `vault`: type a search, then a number to copy.
`:ls` lists everything, `:sync` syncs, `:show` reveals secrets, `:q` quits.

**Stop typing the password** — `vault unlock` keeps the vault unlocked for the
terminal *and* the GUI until 30 minutes without use (`--timeout 60` to change),
or `vault lock`.

## Tab completion

Press **Tab** to complete commands, groups, entries and fields:
`vault wis⇥` → `globex/`, `vault globex/api⇥` → `globex/api-server`,
`vault globex/api-server/⇥` → `logs  password  ssh`.

Entry names are only offered while the vault is unlocked (`vault unlock` or the
GUI); when locked, only commands complete. Set it up once per laptop:

| Shell | Run once |
|---|---|
| bash (Linux) | `echo 'eval "$(vault completion bash)"' >> ~/.bashrc` |
| zsh (macOS default) | `echo 'eval "$(vault completion zsh)"' >> ~/.zshrc` |
| fish | `vault completion fish > ~/.config/fish/completions/vault.fish` |
| PowerShell (Windows) | `Add-Content $PROFILE 'vault completion powershell \| Out-String \| Invoke-Expression'` |

Then open a new terminal. (PowerShell: if `$PROFILE` doesn't exist yet, run
`New-Item -Force $PROFILE` first.)

## GUI

`vault gui` opens it in your browser (or double-click `vaultw.exe` on
Windows). Type to filter, ↑/↓ to move, **Enter** or click copies, `/` jumps to
search, `n` adds an entry, the eye icon reveals a secret. Dark theme; works on narrow windows too.

Tip: bind a system keyboard shortcut to `vault gui` (Windows: shortcut
properties → Shortcut key; macOS: Shortcuts app → Run Shell Script; GNOME:
Settings → Keyboard → Custom Shortcuts).

## Other commands

```
vault status            where the data lives, sync repo, last sync
vault remote <url>      add or change the sync repo later
vault passwd            change the master password (other laptops will ask for the new one)
vault export [group]    print as plain text (careful)
```

Data lives in `%AppData%\vault` (Windows), `~/Library/Application Support/vault`
(macOS) or `~/.config/vault` (Linux); set `VAULT_HOME` to move it.

## Build from source

Go 1.24+, standard library only — no dependencies.

```
go test ./...
./build.sh          # all platforms into dist/
```

## Releasing a new version

Bump `version` in `main.go`, commit, then tag and push:

```
git tag v0.1.3
git push origin main v0.1.3
```

The `release` GitHub Action tests, builds every platform and publishes the
release; the install commands above always fetch the latest one.
