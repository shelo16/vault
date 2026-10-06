package main

import (
	"bytes"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed gui.html
var guiHTML []byte

// Backend is where commands read and write: either the unlocked background
// agent (no password prompt) or the vault file directly (asks for the password).
type Backend interface {
	Snapshot() (*Vault, error)
	Apply([]Change) (syncErr error, err error)
	Sync() error
	Close()
}

func openBackend() (Backend, error) {
	if ag := findAgent(); ag != nil {
		if st, err := ag.status(); err == nil && st.Unlocked {
			return ag, nil
		}
	}
	if !fileExists(vaultFile()) {
		return nil, errNotInit
	}
	for attempt := 0; ; attempt++ {
		pw, err := masterPassword("Master password: ")
		if err != nil {
			return nil, err
		}
		s, err := unlockLocal(pw)
		if errors.Is(err, errBadPassword) && attempt < 2 && os.Getenv("VAULT_PASSWORD") == "" {
			status("wrong password, try again")
			continue
		}
		if err != nil {
			return nil, err
		}
		s.ask = askHidden
		return &localBackend{s}, nil
	}
}

type localBackend struct{ s *Session }

func (l *localBackend) Snapshot() (*Vault, error)       { return l.s.V, nil }
func (l *localBackend) Apply(c []Change) (error, error) { return l.s.Apply(c) }
func (l *localBackend) Sync() error                     { return l.s.Sync() }
func (l *localBackend) Close()                          {}

// ---- agent client ----

type agentInfo struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

type agentStatus struct {
	Unlocked  bool   `json:"unlocked"`
	Rev       int    `json:"rev"`
	HasRemote bool   `json:"hasRemote"`
	Syncing   bool   `json:"syncing"`
	SyncError string `json:"syncError"`
	LastSync  int64  `json:"lastSync"`
	Version   string `json:"version"`
	Host      string `json:"host"`
}

type agentClient struct {
	info agentInfo
	hc   *http.Client
}

func (a *agentClient) url() string { return fmt.Sprintf("http://127.0.0.1:%d", a.info.Port) }

func (a *agentClient) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.url()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", a.info.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != 200 {
		var e struct{ Error string }
		json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (a *agentClient) status() (agentStatus, error) {
	var st agentStatus
	err := a.call("GET", "/api/status", nil, &st)
	return st, err
}

func findAgent() *agentClient {
	b, err := os.ReadFile(agentFile())
	if err != nil {
		return nil
	}
	var info agentInfo
	if json.Unmarshal(b, &info) != nil || info.Port == 0 {
		return nil
	}
	a := &agentClient{info: info, hc: &http.Client{Timeout: 2 * time.Second}}
	if _, err := a.status(); err != nil {
		return nil
	}
	a.hc.Timeout = 2 * time.Minute // syncs can take a moment
	return a
}

type wireEntry struct {
	Path   string  `json:"path"`
	Fields []Field `json:"fields"`
}

func (a *agentClient) Snapshot() (*Vault, error) {
	var out struct{ Entries []wireEntry }
	if err := a.call("GET", "/api/entries", nil, &out); err != nil {
		return nil, err
	}
	v := newVault()
	for _, e := range out.Entries {
		v.Entries[e.Path] = &Entry{Fields: e.Fields}
	}
	return v, nil
}

func (a *agentClient) Apply(chs []Change) (error, error) {
	var out struct{ SyncError string }
	if err := a.call("POST", "/api/apply", map[string]any{"changes": chs}, &out); err != nil {
		return nil, err
	}
	if out.SyncError != "" {
		return errors.New(out.SyncError), nil
	}
	return nil, nil
}

func (a *agentClient) Sync() error { return a.call("POST", "/api/sync", nil, nil) }
func (a *agentClient) Close()      {}

func startAgent(timeoutMin int) (*agentClient, error) {
	if !fileExists(vaultFile()) {
		return nil, errNotInit
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "__agent", "--timeout", strconv.Itoa(timeoutMin))
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	for i := 0; i < 100; i++ {
		time.Sleep(50 * time.Millisecond)
		if a := findAgent(); a != nil && a.info.PID == pid {
			return a, nil
		}
	}
	return nil, errors.New("the background helper didn't start")
}

func ensureAgent(timeoutMin int) (*agentClient, error) {
	if a := findAgent(); a != nil {
		return a, nil
	}
	return startAgent(timeoutMin)
}

func cmdUnlock(o options) error {
	a, err := ensureAgent(o.timeout)
	if err != nil {
		return err
	}
	if st, _ := a.status(); st.Unlocked {
		status("already unlocked")
		return nil
	}
	for attempt := 0; ; attempt++ {
		pw, err := masterPassword("Master password: ")
		if err != nil {
			return err
		}
		err = a.call("POST", "/api/unlock", map[string]string{"password": pw}, nil)
		if err != nil && strings.Contains(err.Error(), "wrong") && attempt < 2 && os.Getenv("VAULT_PASSWORD") == "" {
			status("wrong password, try again")
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	status("unlocked — no password needed (terminal or GUI) until %d min without use, or `vault lock`", o.timeout)
	return nil
}

func cmdLock() error {
	a := findAgent()
	if a == nil {
		status("already locked")
		return nil
	}
	if err := a.call("POST", "/api/lock", nil, nil); err != nil {
		return err
	}
	status("locked")
	return nil
}

// killAgent terminates the running agent process (if any) and removes agent.json.
// Returns true if a process was found and signalled.
func killAgent() bool {
	b, err := os.ReadFile(agentFile())
	if err != nil {
		return false
	}
	var info agentInfo
	if json.Unmarshal(b, &info) != nil || info.PID == 0 {
		return false
	}
	proc, err := os.FindProcess(info.PID)
	if err != nil {
		return false
	}
	killed := proc.Kill() == nil
	os.Remove(agentFile())
	return killed
}

func cmdRestart() error {
	was := findAgent() != nil
	killAgent()
	if was {
		time.Sleep(300 * time.Millisecond)
	}
	if _, err := startAgent(30); err != nil {
		return err
	}
	if was {
		status("agent restarted — vault is locked, run `vault unlock` or `vault gui`")
	} else {
		status("agent started — run `vault unlock` or `vault gui`")
	}
	return nil
}

func cmdGui(o options) error {
	a, err := ensureAgent(o.timeout)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/#%s", a.url(), a.info.Token)
	if err := openBrowser(u); err != nil {
		status("open this in your browser: %s", u)
		return nil
	}
	status("opened vault in your browser")
	return nil
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	hideWindow(cmd)
	return cmd.Start()
}

// ---- agent server ----

type agentServer struct {
	mu         sync.Mutex
	s          *Session
	token      string
	port       int
	lockAfter  time.Duration
	lastActive time.Time
	lastSeen   time.Time
	rev        int
	syncing    bool
	syncErr    string
	lastSync   time.Time
	hasRemote  bool
}

func runAgent(args []string) {
	nonInteractive = true
	timeout := 30
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--timeout" {
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
				timeout = n
			}
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(1)
	}
	now := time.Now()
	a := &agentServer{
		token:      hex.EncodeToString(randBytes(32)),
		port:       ln.Addr().(*net.TCPAddr).Port,
		lockAfter:  time.Duration(timeout) * time.Minute,
		lastActive: now,
		lastSeen:   now,
	}
	info, _ := json.Marshal(agentInfo{Port: a.port, Token: a.token, PID: os.Getpid()})
	os.MkdirAll(homeDir(), 0o700)
	if err := writeFileAtomic(agentFile(), info); err != nil {
		os.Exit(1)
	}
	go a.watch()
	srv := &http.Server{Handler: a, ReadHeaderTimeout: 10 * time.Second}
	srv.Serve(ln)
}

func (a *agentServer) watch() {
	for range time.Tick(10 * time.Second) {
		a.mu.Lock()
		if a.s != nil && time.Since(a.lastActive) > a.lockAfter {
			a.s = nil
			a.rev++
		}
		exit := a.s == nil && !a.syncing && time.Since(a.lastSeen) > 10*time.Minute
		a.mu.Unlock()
		if exit {
			a.exit()
		}
	}
}

func (a *agentServer) exit() {
	if b, err := os.ReadFile(agentFile()); err == nil {
		var info agentInfo
		if json.Unmarshal(b, &info) == nil && info.PID == os.Getpid() {
			os.Remove(agentFile())
		}
	}
	os.Exit(0)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (a *agentServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only answer requests addressed to this exact local origin (blocks DNS rebinding).
	if r.Host != fmt.Sprintf("127.0.0.1:%d", a.port) && r.Host != fmt.Sprintf("localhost:%d", a.port) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/" {
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Write(guiHTML)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Vault-Token")), []byte(a.token)) != 1 {
		fail(w, http.StatusUnauthorized, "not authorised — reopen with `vault gui`")
		return
	}
	if o := r.Header.Get("Origin"); o != "" && o != fmt.Sprintf("http://127.0.0.1:%d", a.port) && o != fmt.Sprintf("http://localhost:%d", a.port) {
		fail(w, http.StatusForbidden, "bad origin")
		return
	}
	var body struct {
		Password string   `json:"password"`
		Changes  []Change `json:"changes"`
		Path     string   `json:"path"`
		Key      string   `json:"key"`
		TTL      *int     `json:"ttl"`
	}
	if r.Method == "POST" && r.ContentLength != 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&body); err != nil && err != io.EOF {
			fail(w, 400, "bad request")
			return
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastSeen = time.Now()
	route := r.Method + " " + r.URL.Path
	if route != "GET /api/status" {
		a.lastActive = time.Now()
	}

	switch route {
	case "GET /api/status":
		var ls int64
		if !a.lastSync.IsZero() {
			ls = a.lastSync.UnixMilli()
		}
		writeJSON(w, 200, agentStatus{Unlocked: a.s != nil, Rev: a.rev, HasRemote: a.hasRemote, Syncing: a.syncing,
			SyncError: a.syncErr, LastSync: ls, Version: version, Host: hostname()})
		return
	case "POST /api/unlock":
		if a.s != nil {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		s, err := unlockLocal(body.Password)
		if err != nil {
			time.Sleep(400 * time.Millisecond)
			fail(w, 401, err.Error())
			return
		}
		a.s = s
		a.rev++
		a.hasRemote = hasRemote()
		if a.hasRemote {
			a.syncing = true
			go a.backgroundSync()
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	case "POST /api/lock":
		a.s = nil
		a.rev++
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}

	if a.s == nil {
		fail(w, 423, "locked")
		return
	}
	switch route {
	case "GET /api/entries":
		v := a.s.V
		out := []wireEntry{}
		for _, p := range v.Paths() {
			out = append(out, wireEntry{Path: p, Fields: v.Entries[p].Fields})
		}
		sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path) })
		writeJSON(w, 200, map[string]any{"entries": out})
	case "POST /api/apply":
		for _, c := range body.Changes {
			if cleanPath(c.Path) == "" {
				fail(w, 400, "every entry needs a name")
				return
			}
			for _, f := range c.Fields {
				if strings.TrimSpace(f.Key) == "" {
					fail(w, 400, "every field needs a name")
					return
				}
			}
		}
		syncErr, err := a.s.Apply(body.Changes)
		a.rev++
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		a.noteSync(syncErr)
		msg := ""
		if syncErr != nil && !errors.Is(syncErr, errNoRemote) {
			msg = syncErr.Error()
		}
		writeJSON(w, 200, map[string]string{"syncError": msg})
	case "POST /api/sync":
		err := a.s.Sync()
		a.rev++
		a.noteSync(err)
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	case "POST /api/copy":
		_, e := a.s.V.Lookup(body.Path)
		if e == nil {
			fail(w, 404, "no such entry")
			return
		}
		fi := fieldIndex(e, body.Key)
		if fi < 0 {
			fail(w, 404, "no such field")
			return
		}
		ttl := 30
		if body.TTL != nil {
			ttl = *body.TTL
		}
		if err := copyToClipboard(e.Fields[fi].Value, ttl); err != nil {
			fail(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]int{"ttl": ttl})
	default:
		http.NotFound(w, r)
	}
}

func (a *agentServer) noteSync(err error) {
	if errors.Is(err, errNoRemote) {
		a.hasRemote = false
		a.syncErr = ""
		return
	}
	a.hasRemote = true
	if err != nil {
		a.syncErr = err.Error()
		return
	}
	a.syncErr = ""
	a.lastSync = time.Now()
}

func (a *agentServer) backgroundSync() {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() { a.syncing = false }()
	if a.s == nil {
		return
	}
	err := a.s.Sync()
	a.noteSync(err)
	a.rev++
}
