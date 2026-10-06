# vault — project context

Personal encrypted notebook for URLs, passwords, SSH logins, build commands and runbooks.
Owner: shelo16. Program repo: github.com/shelo16/vault (public, code only).
Data repo: github.com/shelo16/vault-data (private, holds only the encrypted vault.enc).
Used on 3 laptops: Linux (bash), macOS (zsh), Windows (PowerShell).

## Architecture (Go 1.24, standard library only — no third-party deps, keep it that way)
- model.go     Vault/Entry/Field types, per-entry merge (newest `Updated` wins, tombstones for deletes),
  Resolve() lookup (exact path, dotted path, group listing, fuzzy word match), text import/export format
- store.go     Encryption: PBKDF2-SHA256 (600k) + AES-256-GCM, one file vault.enc. Session, save, git commit/sync.
  Sync = fetch origin/main → decrypt remote → merge per entry → reset --soft → commit → push (retries 3x)
- agent.go     `vault unlock` starts a background agent on 127.0.0.1 (random port + token in agent.json,
  auto-lock after idle). CLI uses it when unlocked; it also serves the GUI (gui.html, embedded)
- gui.html     Single-file dark-theme GUI (vanilla JS), talks to the agent's /api/* with X-Vault-Token
- main.go      CLI commands, interactive mode, editor-based add/edit
- clip.go      Clipboard via OS tools (pbcopy / PowerShell / wl-copy, xclip, xsel); auto-clear helper (`__clip`)
- complete.go  Tab completion (`vault completion bash|zsh|fish|powershell`, hidden `__complete`);
  names only offered when the agent is unlocked
- sys_unix.go / sys_windows.go  no-echo password input, process detaching
- install.sh / install.ps1      one-line installers that pull from the latest GitHub release
- .github/workflows/release.yml tag v* → test, build all platforms (build.sh), publish release + installers

Data dir: ~/.config/vault (Linux), ~/Library/Application Support/vault (macOS), %AppData%\vault (Windows);
VAULT_HOME overrides. VAULT_PASSWORD env is honoured for tests/scripts.

## Working rules
- Never commit real data, company names, hostnames or IPs — examples use acme/globex/10.0.0.x.
- Run `go vet ./...`, `GOOS=windows go vet ./...`, `go test ./...` before committing.
- Release: bump `version` in main.go, commit, `git tag vX.Y.Z && git push origin main vX.Y.Z`.
- Users update by re-running the install one-liner (README).

## Status
- v0.1.3 is tagged locally; first push to GitHub pending (repo needed creating).
- Linux laptop: vault-data initialised and data imported; running 0.1.2 installed by hand.
- Mac and Windows laptops: not installed yet.

## Ideas not done yet
- `vault update` self-update command
- true global hotkey / native window (currently browser GUI + OS shortcut to `vault gui`)