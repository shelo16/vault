package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Encryption: PBKDF2-HMAC-SHA256 (600k iterations, OWASP 2023+) derives a
// 256-bit key from the master password; the vault JSON is sealed with AES-256-GCM.
// Only this encrypted file is ever written to disk or pushed to git.
const kdfIter = 600_000

var aad = []byte("vault-cli/v1")

type encFile struct {
	V     int    `json:"v"`
	KDF   string `json:"kdf"`
	Iter  int    `json:"iter"`
	Salt  []byte `json:"salt"`
	Nonce []byte `json:"nonce"`
	Data  []byte `json:"data"`
}

var (
	errBadPassword  = errors.New("wrong master password")
	errNotInit      = errors.New("no vault on this device yet — run `vault init` (new vault) or `vault init <git-url>` (join your existing one)")
	errNoRemote     = errors.New("no sync remote configured — run `vault remote <git-url>`")
	errPushRejected = errors.New("push rejected")
	nonInteractive  bool // set in the background agent: never prompt
)

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func deriveKey(pw string, salt []byte, iter int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pw, salt, iter, 32)
}

// ---- locations ----

func homeDir() string {
	if d := os.Getenv("VAULT_HOME"); d != "" {
		return d
	}
	c, err := os.UserConfigDir()
	if err != nil {
		h, _ := os.UserHomeDir()
		c = h
	}
	return filepath.Join(c, "vault")
}

func dataDir() string   { return filepath.Join(homeDir(), "data") }
func vaultFile() string { return filepath.Join(dataDir(), "vault.enc") }
func agentFile() string { return filepath.Join(homeDir(), "agent.json") }

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ---- session: an unlocked vault held in memory ----

type Session struct {
	pw   string
	salt []byte
	iter int
	key  []byte
	V    *Vault
	ask  func(prompt string) (string, error) // nil = cannot prompt
}

func newSession(pw string) (*Session, error) {
	s := &Session{pw: pw, salt: randBytes(16), iter: kdfIter, V: newVault()}
	k, err := deriveKey(pw, s.salt, s.iter)
	if err != nil {
		return nil, err
	}
	s.key = k
	return s, nil
}

func (s *Session) decrypt(data []byte, pw string) (*Vault, *encFile, []byte, error) {
	var f encFile
	if err := json.Unmarshal(data, &f); err != nil || f.V != 1 {
		return nil, nil, nil, errors.New("vault file is corrupted or from an unknown version")
	}
	key := s.key
	if key == nil || pw != s.pw || !bytes.Equal(f.Salt, s.salt) || f.Iter != s.iter {
		k, err := deriveKey(pw, f.Salt, f.Iter)
		if err != nil {
			return nil, nil, nil, err
		}
		key = k
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, nil, err
	}
	pt, err := gcm.Open(nil, f.Nonce, f.Data, aad)
	if err != nil {
		return nil, nil, nil, errBadPassword
	}
	v := newVault()
	if err := json.Unmarshal(pt, v); err != nil {
		return nil, nil, nil, err
	}
	if v.Entries == nil {
		v.Entries = map[string]*Entry{}
	}
	return v, &f, key, nil
}

func unlockLocal(pw string) (*Session, error) {
	data, err := os.ReadFile(vaultFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotInit
	}
	if err != nil {
		return nil, err
	}
	s := &Session{pw: pw}
	v, f, key, err := s.decrypt(data, pw)
	if err != nil {
		return nil, err
	}
	s.V, s.salt, s.iter, s.key = v, f.Salt, f.Iter, key
	return s, nil
}

func (s *Session) seal() ([]byte, error) {
	pt, err := json.Marshal(s.V)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := randBytes(gcm.NonceSize())
	f := encFile{V: 1, KDF: "pbkdf2-sha256", Iter: s.iter, Salt: s.salt, Nonce: nonce, Data: gcm.Seal(nil, nonce, pt, aad)}
	return json.MarshalIndent(f, "", "  ")
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Session) save() error {
	b, err := s.seal()
	if err != nil {
		return err
	}
	return writeFileAtomic(vaultFile(), b)
}

// Apply edits the vault, saves it, commits locally and syncs if a remote is set.
// A sync failure is returned separately: the change is still safely saved.
func (s *Session) Apply(chs []Change) (syncErr error, err error) {
	s.V.Apply(chs)
	if err := s.save(); err != nil {
		return nil, err
	}
	if err := gitCommit("update from " + hostname()); err != nil {
		return nil, err
	}
	if hasRemote() {
		return s.Sync(), nil
	}
	return nil, nil
}

func (s *Session) ChangePassword(pw string) error {
	salt := randBytes(16)
	key, err := deriveKey(pw, salt, kdfIter)
	if err != nil {
		return err
	}
	s.pw, s.salt, s.iter, s.key = pw, salt, kdfIter, key
	if err := s.save(); err != nil {
		return err
	}
	return gitCommit("change master password")
}

// ---- git ----

func runGit(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dataDir()}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if nonInteractive {
		hideWindow(cmd)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	} else {
		cmd.Stdin = os.Stdin
	}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg := strings.TrimSpace(errb.String())
			if msg == "" {
				msg = err.Error()
			}
			return out.Bytes(), fmt.Errorf("git %s: %s", args[0], msg)
		}
		return nil, fmt.Errorf("git is required for sync but could not be run: %w", err)
	}
	return out.Bytes(), nil
}

func gitCommit(msg string) error {
	if _, err := runGit("add", "-A"); err != nil {
		return err
	}
	if _, err := runGit("diff", "--cached", "--quiet"); err == nil {
		return nil // nothing to commit
	}
	_, err := runGit("-c", "user.name=vault", "-c", "user.email=vault@"+hostname(), "commit", "-q", "-m", msg)
	return err
}

func hasRemote() bool {
	_, err := runGit("remote", "get-url", "origin")
	return err == nil
}

func remoteURL() string {
	out, err := runGit("remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func remoteHasVault() (bool, error) {
	if _, err := runGit("fetch", "-q", "origin"); err != nil {
		return false, err
	}
	_, err := runGit("rev-parse", "--verify", "-q", "origin/main")
	return err == nil, nil
}

// Sync merges the remote copy entry-by-entry (newest edit of each entry wins,
// deletions included) and pushes the result. History stays linear.
func (s *Session) Sync() error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = s.syncOnce(); err == nil || !errors.Is(err, errPushRejected) {
			return err
		}
	}
	return err
}

func (s *Session) syncOnce() error {
	if !hasRemote() {
		return errNoRemote
	}
	remote, err := remoteHasVault()
	if err != nil {
		return err
	}
	if remote {
		data, err := runGit("show", "origin/main:vault.enc")
		if err == nil {
			rv, f, key, derr := s.decrypt(data, s.pw)
			if errors.Is(derr, errBadPassword) {
				if s.ask == nil {
					return errors.New("the synced vault has a different master password (changed on another device?) — run `vault sync` in a terminal to enter it")
				}
				pw, aerr := s.ask("The synced vault has a different master password (changed on another device?).\nEnter it: ")
				if aerr != nil {
					return aerr
				}
				rv, f, key, derr = s.decrypt(data, pw)
				if derr != nil {
					return derr
				}
				s.pw = pw
			} else if derr != nil {
				return derr
			}
			localNew := cloneVault(rv).Merge(s.V)
			s.V.Merge(rv)
			s.salt, s.iter, s.key = f.Salt, f.Iter, key
			if localNew {
				if err := s.save(); err != nil {
					return err
				}
			} else if err := writeFileAtomic(vaultFile(), data); err != nil {
				return err
			}
		}
		if _, err := runGit("reset", "-q", "--soft", "origin/main"); err != nil {
			return err
		}
	}
	if err := gitCommit("sync from " + hostname()); err != nil {
		return err
	}
	if _, err := runGit("push", "-q", "origin", "HEAD:main"); err != nil {
		m := err.Error()
		if strings.Contains(m, "rejected") || strings.Contains(m, "fetch first") || strings.Contains(m, "non-fast-forward") {
			return fmt.Errorf("%w: %v", errPushRejected, err)
		}
		return err
	}
	return nil
}

// initRepo prepares the data folder as a git repo on branch main.
func initRepo() error {
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	if fileExists(filepath.Join(dataDir(), ".git")) {
		return nil
	}
	if _, err := runGit("init", "-q", "-b", "main"); err != nil {
		if _, err := runGit("init", "-q"); err != nil {
			return err
		}
		if _, err := runGit("symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
			return err
		}
	}
	return nil
}

func setRemote(url string) error {
	if hasRemote() {
		_, err := runGit("remote", "set-url", "origin", url)
		return err
	}
	_, err := runGit("remote", "add", "origin", url)
	return err
}
