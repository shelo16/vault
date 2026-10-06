package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

var (
	version    = "0.1.6"
	defaultCmd = "" // set to "gui" for the windowless Windows build (vaultw.exe)
)

const usageText = `vault — your encrypted notebook for URLs, passwords, SSH logins and commands

LOOK THINGS UP
  vault                          interactive mode: type to search, number to copy
  vault <key>                    show / copy. Examples:
      vault acme                     everything under acme/
      vault acme/prod-db             the entry (pick a field to copy)
      vault acme.prod-db.password    copy that field
      vault globex-build-console         fuzzy: punctuation ignored, copies the command
      vault prod db pass                 fuzzy: every word must match
  vault get <key>                same as above (use if your key is a command name)
  vault find <text>              search values too (IPs, hostnames, ...)
  vault ls [group]               list entries

  -p, --print     print the raw value instead of copying, e.g.  ssh $(vault -p globex/api-server/ssh)
  -s, --show      show secrets instead of ********
  --ttl N         clear the clipboard after N seconds (default 30, 0 = never)

EDIT
  vault add <path>               new entry (opens your editor)
  vault edit [path|group]        edit entries as text (opens your editor)
  vault set <path/field> [value] set one field (prompts hidden for secrets)   --secret to force
  vault rm <path|group>          delete
  vault mv <old> <new>           rename an entry or a whole group
  vault import <file>            bulk import from the text format (see README)
  vault export [group]           print as text — PLAIN TEXT, careful

GUI & SESSION
  vault gui                      open the GUI in your browser (local only, password-locked)
  vault unlock [--timeout MIN]   stay unlocked (default 30 min idle) — no password per command
  vault lock                     lock now

SYNC & SETUP
  vault init [git-url]           create a vault, or join your existing one from git
  vault remote <git-url>         set/change the sync repo (private GitHub repo recommended)
  vault sync                     sync now (edits sync automatically)
  vault passwd                   change the master password
  vault status                   where things are, sync state
  vault update                   update vault to the latest release
  vault restart                  restart the background agent (picks up a new binary)
  vault completion <shell>       tab completion: bash, zsh, fish or powershell (see README)
`

type options struct {
	print, reveal, secret, yes bool
	ttl, timeout               int
}

func parseFlags(args []string) (options, []string, error) {
	o := options{ttl: 30, timeout: 30}
	var rest []string
	num := func(i int, name string) (int, error) {
		if i+1 >= len(args) {
			return 0, fmt.Errorf("%s needs a number", name)
		}
		n, err := strconv.Atoi(args[i+1])
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%s needs a number", name)
		}
		return n, nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var err error
		switch a {
		case "--":
			rest = append(rest, args[i+1:]...)
			i = len(args)
		case "-p", "--print":
			o.print = true
		case "-s", "--show":
			o.reveal = true
		case "--secret":
			o.secret = true
		case "-y", "--yes":
			o.yes = true
		case "--ttl":
			o.ttl, err = num(i, a)
			i++
		case "--timeout":
			o.timeout, err = num(i, a)
			i++
		case "-h", "--help":
			rest = append([]string{"help"}, rest...)
		case "-v", "--version":
			rest = append([]string{"version"}, rest...)
		default:
			rest = append(rest, a)
		}
		if err != nil {
			return o, nil, err
		}
	}
	return o, rest, nil
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "__clip":
			runClipChild(os.Args[2:])
			return
		case "__agent":
			runAgent(os.Args[2:])
			return
		case "__complete":
			runComplete(os.Args[2:])
			return
		}
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "vault:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	o, rest, err := parseFlags(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		if defaultCmd == "gui" {
			return cmdGui(o)
		}
		return interactive(o)
	}
	cmd, a := rest[0], rest[1:]
	switch cmd {
	case "help":
		fmt.Print(usageText)
		return nil
	case "version":
		fmt.Println("vault", version, runtime.GOOS+"/"+runtime.GOARCH)
		return nil
	case "init":
		return cmdInit(a)
	case "remote":
		return cmdRemote(a)
	case "status":
		return cmdStatus()
	case "gui":
		return cmdGui(o)
	case "unlock":
		return cmdUnlock(o)
	case "lock":
		return cmdLock()
	case "passwd":
		return cmdPasswd()
	case "completion":
		return cmdCompletion(a)
	case "update":
		return cmdUpdate()
	case "restart":
		return cmdRestart()
	}

	b, err := openBackend()
	if err != nil {
		return err
	}
	defer b.Close()
	switch cmd {
	case "get":
		return lookupOnce(b, strings.Join(a, " "), o)
	case "ls", "list":
		return cmdLs(b, a)
	case "find", "search":
		return cmdFind(b, a, o)
	case "sync":
		if err := b.Sync(); err != nil {
			return err
		}
		v, _ := b.Snapshot()
		status("synced — %d entries", len(v.Paths()))
		return nil
	case "add", "new":
		return cmdAdd(b, a)
	case "edit":
		return cmdEdit(b, a)
	case "set":
		return cmdSet(b, a, o)
	case "rm", "del", "delete":
		return cmdRm(b, a, o)
	case "mv", "rename":
		return cmdMv(b, a)
	case "import":
		return cmdImport(b, a, o)
	case "export":
		return cmdExport(b, a)
	}
	return lookupOnce(b, strings.Join(rest, " "), o)
}

// ---- terminal helpers ----

var stdin = bufio.NewReader(os.Stdin)

func readLine(prompt string) (string, error) {
	if prompt != "" {
		fmt.Fprint(os.Stderr, prompt)
	}
	s, err := stdin.ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func canPrompt() bool { return isTerminal(os.Stdin) }

func confirm(prompt string, o options) bool {
	if o.yes {
		return true
	}
	if !canPrompt() {
		return false
	}
	l, _ := readLine(prompt + " [y/N] ")
	l = strings.ToLower(strings.TrimSpace(l))
	return l == "y" || l == "yes"
}

func status(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

// masterPassword reads the master password. VAULT_PASSWORD is honoured for
// scripting/testing, but typing it is safer.
func masterPassword(prompt string) (string, error) {
	if pw := os.Getenv("VAULT_PASSWORD"); pw != "" {
		return pw, nil
	}
	if !canPrompt() {
		return "", errors.New("vault is locked and there is no terminal to ask for the password — run `vault unlock` first")
	}
	return readPasswordTTY(prompt)
}

func askHidden(prompt string) (string, error) {
	if !canPrompt() {
		return "", errors.New("no terminal to ask for a password")
	}
	return readPasswordTTY(prompt)
}

// ---- display ----

const mask = "********"

func shown(f Field, reveal bool) string {
	if f.Secret && !reveal {
		return mask
	}
	return f.Value
}

func printEntry(w io.Writer, path string, e *Entry, numbered, reveal bool) {
	fmt.Fprintln(w, path)
	kw := 0
	for _, f := range e.Fields {
		if len(f.Key) > kw {
			kw = len(f.Key)
		}
	}
	for i, f := range e.Fields {
		num := ""
		if numbered {
			num = fmt.Sprintf("%2d  ", i+1)
		}
		lines := strings.Split(shown(f, reveal), "\n")
		fmt.Fprintf(w, "  %s%-*s  %s\n", num, kw, f.Key, lines[0])
		pad := strings.Repeat(" ", 2+len(num)+kw+2)
		for _, l := range lines[1:] {
			fmt.Fprintf(w, "%s%s\n", pad, l)
		}
	}
}

func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func printHits(w io.Writer, v *Vault, hits []Hit, numbered, reveal bool) {
	pw := 0
	for _, h := range hits {
		l := len(h.Path)
		if h.Field >= 0 {
			l += 1 + len(v.Entries[h.Path].Fields[h.Field].Key)
		}
		if l > pw {
			pw = l
		}
	}
	lastGroup := ""
	for i, h := range hits {
		g := strings.SplitN(h.Path, "/", 2)[0]
		if i > 0 && g != lastGroup && h.Field < 0 {
			fmt.Fprintln(w)
		}
		lastGroup = g
		num := ""
		if numbered {
			num = fmt.Sprintf("%3d  ", i+1)
		}
		e := v.Entries[h.Path]
		if h.Field < 0 {
			keys := make([]string, len(e.Fields))
			for j, f := range e.Fields {
				keys[j] = f.Key
			}
			fmt.Fprintf(w, "%s%-*s  %s\n", num, pw, h.Path, strings.Join(keys, ", "))
		} else {
			f := e.Fields[h.Field]
			fmt.Fprintf(w, "%s%-*s  %s\n", num, pw, h.Path+"/"+f.Key, preview(shown(f, reveal), 60))
		}
	}
}

// ---- lookup ----

func deliver(v *Vault, h Hit, o options) error {
	e := v.Entries[h.Path]
	f := e.Fields[h.Field]
	if o.print {
		fmt.Println(f.Value)
		return nil
	}
	if err := copyToClipboard(f.Value, o.ttl); err != nil {
		if !f.Secret || o.reveal {
			fmt.Println(f.Value)
		}
		return fmt.Errorf("%v\n  tip: `vault -p %s/%s` prints the value instead", err, h.Path, f.Key)
	}
	if !f.Secret || o.reveal {
		fmt.Println(f.Value)
	}
	clears := ""
	if o.ttl > 0 {
		clears = fmt.Sprintf(" (clears in %ds)", o.ttl)
	}
	status("copied %s/%s%s", h.Path, f.Key, clears)
	return nil
}

// Picker acts on the 1-based number the user picked from the last listing.
// Picking an entry out of a list shows it and returns a picker for its fields.
type Picker func(n int) (Picker, error)

func lookup(v *Vault, q string, o options) (Picker, error) {
	r := v.Resolve(q)
	switch r.Kind {
	case NoMatch:
		return nil, fmt.Errorf("nothing matches %q (try `vault find %s`)", q, q)
	case FieldMatch:
		return nil, deliver(v, r.Hit, o)
	case EntryMatch:
		return showEntry(v, r.Hit.Path, o)
	}
	if len(r.List) == 0 {
		return nil, errors.New("vault is empty — add something with `vault add <group/name>` or `vault import <file>`")
	}
	if o.print {
		printHits(os.Stderr, v, r.List, false, false)
		return nil, fmt.Errorf("%q matches %d things — be more specific", q, len(r.List))
	}
	printHits(os.Stdout, v, r.List, true, o.reveal)
	hits := r.List
	return func(n int) (Picker, error) {
		if n < 1 || n > len(hits) {
			return nil, fmt.Errorf("pick 1-%d", len(hits))
		}
		h := hits[n-1]
		if h.Field >= 0 {
			return nil, deliver(v, h, o)
		}
		return showEntry(v, h.Path, o)
	}, nil
}

func showEntry(v *Vault, path string, o options) (Picker, error) {
	e := v.Entries[path]
	if len(e.Fields) == 1 {
		return nil, deliver(v, Hit{path, 0}, o)
	}
	if o.print {
		return nil, fmt.Errorf("%s has several fields — add one, e.g. `vault -p %s/%s`", path, path, e.Fields[0].Key)
	}
	printEntry(os.Stdout, path, e, true, o.reveal)
	if len(e.Fields) == 0 {
		return nil, nil
	}
	return func(n int) (Picker, error) {
		if n < 1 || n > len(e.Fields) {
			return nil, fmt.Errorf("pick 1-%d", len(e.Fields))
		}
		return nil, deliver(v, Hit{path, n - 1}, o)
	}, nil
}

func lookupOnce(b Backend, q string, o options) error {
	v, err := b.Snapshot()
	if err != nil {
		return err
	}
	pick, err := lookup(v, q, o)
	if err != nil {
		return err
	}
	for pick != nil && canPrompt() && isTerminal(os.Stdout) {
		l, rerr := readLine("copy # (enter to skip): ")
		n, cerr := strconv.Atoi(strings.TrimSpace(l))
		if rerr != nil || cerr != nil {
			return nil
		}
		next, perr := pick(n)
		if perr != nil {
			fmt.Fprintln(os.Stderr, perr)
			continue
		}
		pick = next
	}
	return nil
}

func interactive(o options) error {
	b, err := openBackend()
	if err != nil {
		return err
	}
	defer b.Close()
	status("vault %s — type to search · a number copies it · :ls :sync :help :q", version)
	var pick Picker
	for {
		line, err := readLine("\nvault> ")
		if err != nil {
			fmt.Fprintln(os.Stderr)
			return nil
		}
		line = strings.TrimSpace(line)
		switch line {
		case "":
			continue
		case ":q", "q", ":quit", "quit", "exit":
			return nil
		case ":help", "?":
			status("search: words, or keys like globex/build-console, acme.prod-db.password\n" +
				"number: copy that item   :ls list all   :sync sync now   :show/:hide secrets   :q quit")
			continue
		case ":show":
			o.reveal = true
			continue
		case ":hide":
			o.reveal = false
			continue
		case ":sync":
			if err := b.Sync(); err != nil {
				status("sync failed: %v", err)
			} else {
				status("synced")
			}
			continue
		}
		if n, cerr := strconv.Atoi(line); cerr == nil && pick != nil {
			next, perr := pick(n)
			if perr != nil {
				status("%v", perr)
			} else {
				pick = next
			}
			continue
		}
		v, err := b.Snapshot()
		if err != nil {
			return err
		}
		q := line
		if line == ":ls" {
			q = ""
		}
		p, lerr := lookup(v, q, o)
		if lerr != nil {
			status("%v", lerr)
		}
		pick = p
	}
}

// ---- commands ----

func cmdLs(b Backend, a []string) error {
	v, err := b.Snapshot()
	if err != nil {
		return err
	}
	paths := v.Under(strings.Join(a, " "))
	if len(paths) == 0 {
		if len(a) == 0 {
			status("vault is empty — add something with `vault add <group/name>` or `vault import <file>`")
			return nil
		}
		return fmt.Errorf("nothing under %q", strings.Join(a, " "))
	}
	printHits(os.Stdout, v, entryHits(paths), false, false)
	return nil
}

func cmdFind(b Backend, a []string, o options) error {
	v, err := b.Snapshot()
	if err != nil {
		return err
	}
	hits := v.Find(strings.Join(a, " "))
	if len(hits) == 0 {
		return fmt.Errorf("nothing found")
	}
	printHits(os.Stdout, v, hits, true, o.reveal)
	if canPrompt() && isTerminal(os.Stdout) && !o.print {
		l, _ := readLine("copy # (enter to skip): ")
		if n, err := strconv.Atoi(strings.TrimSpace(l)); err == nil {
			if n < 1 || n > len(hits) {
				return fmt.Errorf("pick 1-%d", len(hits))
			}
			return deliver(v, hits[n-1], o)
		}
	}
	return nil
}

func reportSync(syncErr error) {
	if syncErr != nil && !errors.Is(syncErr, errNoRemote) {
		status("saved on this device, but sync failed: %v\n  (it will sync next time; or run `vault sync`)", syncErr)
	}
}

func applyChanges(b Backend, chs []Change, msg string) error {
	if len(chs) == 0 {
		status("no changes")
		return nil
	}
	syncErr, err := b.Apply(chs)
	if err != nil {
		return err
	}
	status("%s", msg)
	reportSync(syncErr)
	return nil
}

func editorCommand() []string {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if e := strings.Fields(os.Getenv(env)); len(e) > 0 {
			return e
		}
	}
	if runtime.GOOS == "windows" {
		return []string{"notepad"}
	}
	for _, c := range []string{"nano", "vim", "vi"} {
		if _, err := exec.LookPath(c); err == nil {
			return []string{c}
		}
	}
	return []string{"vi"}
}

// editText lets the user edit text in their editor via a private temp file
// that is wiped right after.
func editText(initial string) (string, error) {
	dir, err := os.MkdirTemp("", "vault-edit-")
	if err != nil {
		return "", err
	}
	path := dir + string(os.PathSeparator) + "vault-edit.txt"
	defer func() {
		if st, err := os.Stat(path); err == nil {
			_ = os.WriteFile(path, make([]byte, st.Size()), 0o600)
		}
		os.RemoveAll(dir)
	}()
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		return "", err
	}
	ed := editorCommand()
	cmd := exec.Command(ed[0], append(ed[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor %q failed: %w (set $EDITOR to choose another)", ed[0], err)
	}
	out, err := os.ReadFile(path)
	return string(out), err
}

const editHelp = `# Lines are  key = value.  Keys containing pass/secret/token are hidden
# automatically; put ! in front of any other key to hide it (!pin = 1234).
# Multi-line values:   note = """
#                      line one
#                      """
# Save and close the editor to apply. Remove a [section] to delete it.
`

// editLoop opens the editor until the text parses (or the user gives up).
func editLoop(text string) ([]Section, bool, error) {
	for {
		out, err := editText(text)
		if err != nil {
			return nil, false, err
		}
		if strings.TrimSpace(out) == strings.TrimSpace(text) {
			return nil, false, nil
		}
		secs, perr := parseText(out)
		if perr == nil {
			return secs, true, nil
		}
		status("%v", perr)
		if !confirm("edit again?", options{}) {
			return nil, false, errors.New("cancelled, nothing changed")
		}
		text = out
	}
}

func cmdAdd(b Backend, a []string) error {
	path := cleanPath(strings.Join(a, " "))
	if path == "" {
		return errors.New("usage: vault add <group/name>   e.g. vault add globex/api-server")
	}
	v, err := b.Snapshot()
	if err != nil {
		return err
	}
	if p, e := v.Lookup(path); e != nil {
		return fmt.Errorf("%s already exists — use `vault edit %s`", p, p)
	}
	tmpl := editHelp + "\n[" + path + "]\nurl = \nusername = \npassword = \n"
	secs, changed, err := editLoop(tmpl)
	if err != nil || !changed {
		return err
	}
	var chs []Change
	for _, s := range secs {
		var fs []Field
		for _, f := range s.Fields {
			if f.Value != "" {
				fs = append(fs, f)
			}
		}
		if len(fs) > 0 {
			chs = append(chs, Change{Path: s.Path, Fields: fs})
		}
	}
	return applyChanges(b, chs, fmt.Sprintf("saved %d entr%s", len(chs), plural(len(chs), "y", "ies")))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func cmdEdit(b Backend, a []string) error {
	v, err := b.Snapshot()
	if err != nil {
		return err
	}
	prefix := strings.Join(a, " ")
	paths := v.Under(prefix)
	if len(paths) == 0 && prefix != "" {
		r := v.Resolve(prefix)
		switch r.Kind {
		case EntryMatch, FieldMatch:
			paths = []string{r.Hit.Path}
		case ListMatch:
			seen := map[string]bool{}
			for _, h := range r.List {
				if !seen[h.Path] {
					seen[h.Path] = true
					paths = append(paths, h.Path)
				}
			}
		}
	}
	if len(paths) == 0 {
		if prefix != "" {
			return fmt.Errorf("nothing matches %q — use `vault add %s` to create it", prefix, cleanPath(prefix))
		}
	}
	secs, changed, err := editLoop(editHelp + "\n" + renderText(v, paths))
	if err != nil || !changed {
		if err == nil {
			status("no changes")
		}
		return err
	}
	chs := diffSections(v, paths, secs)
	return applyChanges(b, chs, fmt.Sprintf("saved %d change%s", len(chs), plural(len(chs), "", "s")))
}

// diffSections turns an edited text back into changes against the original paths.
func diffSections(v *Vault, before []string, secs []Section) []Change {
	var chs []Change
	kept := map[string]bool{}
	for _, s := range secs {
		kept[strings.ToLower(s.Path)] = true
		p, e := v.Lookup(s.Path)
		if e != nil && p == s.Path && fieldsEqual(e.Fields, s.Fields) {
			continue
		}
		chs = append(chs, Change{Path: s.Path, Fields: s.Fields})
	}
	for _, p := range before {
		if !kept[strings.ToLower(p)] {
			chs = append(chs, Change{Path: p, Delete: true})
		}
	}
	return chs
}

func cmdSet(b Backend, a []string, o options) error {
	if len(a) == 0 {
		return errors.New("usage: vault set <group/name/field> [value]")
	}
	full := cleanPath(a[0])
	i := strings.LastIndex(full, "/")
	if i <= 0 {
		return errors.New("give the entry and the field, e.g. vault set globex/api-server/password")
	}
	path, key := full[:i], full[i+1:]
	secret := o.secret || looksSecret(key)
	var val string
	if len(a) > 1 {
		val = strings.Join(a[1:], " ")
	} else {
		var err error
		if secret {
			val, err = askHidden(key + ": ")
		} else {
			val, err = readLine(key + ": ")
		}
		if err != nil {
			return err
		}
	}
	v, err := b.Snapshot()
	if err != nil {
		return err
	}
	var fields []Field
	if p, e := v.Lookup(path); e != nil {
		path = p
		fields = append(fields, e.Fields...)
	}
	if fi := fieldIndex(&Entry{Fields: fields}, key); fi >= 0 {
		fields[fi].Value = val
		fields[fi].Secret = fields[fi].Secret || secret
	} else {
		fields = append(fields, Field{Key: key, Value: val, Secret: secret})
	}
	return applyChanges(b, []Change{{Path: path, Fields: fields}}, "saved "+path+"/"+key)
}

func cmdRm(b Backend, a []string, o options) error {
	q := strings.Join(a, " ")
	if q == "" {
		return errors.New("usage: vault rm <group/name>   or   vault rm <group>")
	}
	v, err := b.Snapshot()
	if err != nil {
		return err
	}
	paths := v.Under(q)
	if len(paths) == 0 {
		return fmt.Errorf("nothing at %q (rm needs the exact path; see `vault ls`)", q)
	}
	printHits(os.Stdout, v, entryHits(paths), false, false)
	if !confirm(fmt.Sprintf("delete %d entr%s?", len(paths), plural(len(paths), "y", "ies")), o) {
		return errors.New("cancelled")
	}
	var chs []Change
	for _, p := range paths {
		chs = append(chs, Change{Path: p, Delete: true})
	}
	return applyChanges(b, chs, "deleted")
}

func cmdMv(b Backend, a []string) error {
	if len(a) != 2 {
		return errors.New("usage: vault mv <old> <new>")
	}
	v, err := b.Snapshot()
	if err != nil {
		return err
	}
	from, to := cleanPath(a[0]), cleanPath(a[1])
	paths := v.Under(from)
	if len(paths) == 0 || to == "" {
		return fmt.Errorf("nothing at %q", from)
	}
	var chs []Change
	for _, p := range paths {
		np := to + p[len(from):]
		if q, e := v.Lookup(np); e != nil && !strings.EqualFold(q, p) {
			return fmt.Errorf("%s already exists", q)
		}
		chs = append(chs, Change{Path: p, Delete: true}, Change{Path: np, Fields: v.Entries[p].Fields})
	}
	return applyChanges(b, chs, fmt.Sprintf("moved %d entr%s", len(paths), plural(len(paths), "y", "ies")))
}

func cmdImport(b Backend, a []string, o options) error {
	if len(a) != 1 {
		return errors.New("usage: vault import <file>   (format: see README / `vault export`)")
	}
	data, err := os.ReadFile(a[0])
	if err != nil {
		return err
	}
	secs, err := parseText(string(data))
	if err != nil {
		return fmt.Errorf("%s: %v", a[0], err)
	}
	v, err := b.Snapshot()
	if err != nil {
		return err
	}
	var chs []Change
	newN, replN := 0, 0
	for _, s := range secs {
		if len(s.Fields) == 0 {
			continue
		}
		if _, e := v.Lookup(s.Path); e != nil {
			replN++
		} else {
			newN++
		}
		chs = append(chs, Change{Path: s.Path, Fields: s.Fields})
	}
	status("%d new, %d existing (will be replaced)", newN, replN)
	if replN > 0 && !confirm("continue?", o) {
		return errors.New("cancelled")
	}
	if err := applyChanges(b, chs, fmt.Sprintf("imported %d entries", len(chs))); err != nil {
		return err
	}
	status("now delete the plain-text file: %s", a[0])
	return nil
}

func cmdExport(b Backend, a []string) error {
	v, err := b.Snapshot()
	if err != nil {
		return err
	}
	paths := v.Under(strings.Join(a, " "))
	if isTerminal(os.Stdout) {
		status("# warning: this is plain text — don't save it anywhere permanent")
	}
	fmt.Print(renderText(v, paths))
	return nil
}

func cmdInit(a []string) error {
	if fileExists(vaultFile()) {
		return fmt.Errorf("this device already has a vault (%s)", dataDir())
	}
	url := ""
	if len(a) > 0 {
		url = a[0]
	}
	if err := initRepo(); err != nil {
		return err
	}
	if url != "" {
		if err := setRemote(url); err != nil {
			return err
		}
		status("checking %s ...", url)
		exists, err := remoteHasVault()
		if err != nil {
			return fmt.Errorf("can't reach the repo: %v\n  check the URL and that this computer can access it (ssh key / gh auth)", err)
		}
		if exists {
			if _, err := runGit("checkout", "-q", "-B", "main", "origin/main"); err != nil {
				return err
			}
			pw, err := masterPassword("Master password: ")
			if err != nil {
				return err
			}
			s, err := unlockLocal(pw)
			if err != nil {
				os.Remove(vaultFile())
				return fmt.Errorf("%v — run `vault init %s` again", err, url)
			}
			status("joined your vault: %d entries. Try `vault` or `vault gui`.", len(s.V.Paths()))
			return nil
		}
		status("the repo is empty — creating a new vault there")
	}
	pw, err := choosePassword()
	if err != nil {
		return err
	}
	s, err := newSession(pw)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dataDir()+string(os.PathSeparator)+".gitattributes", []byte("vault.enc binary\n"), 0o600); err != nil {
		return err
	}
	if err := s.save(); err != nil {
		return err
	}
	if err := gitCommit("create vault"); err != nil {
		return err
	}
	if url != "" {
		if _, err := runGit("push", "-q", "-u", "origin", "HEAD:main"); err != nil {
			return fmt.Errorf("vault created locally, but the first push failed: %v", err)
		}
	}
	status("vault created in %s", dataDir())
	if url == "" {
		status("tip: add sync with `vault remote <git-url>` (a private GitHub repo)")
	}
	status("next: `vault add <group/name>` or `vault import <file>`")
	return nil
}

func choosePassword() (string, error) {
	if pw := os.Getenv("VAULT_PASSWORD"); pw != "" {
		return pw, nil
	}
	status("Choose a master password. It encrypts everything and can NOT be recovered.\nA few random words works well, e.g. 'copper-lantern-river-seven'.")
	for {
		p1, err := askHidden("New master password: ")
		if err != nil {
			return "", err
		}
		if len([]rune(p1)) < 10 {
			status("use at least 10 characters")
			continue
		}
		p2, err := askHidden("Repeat it: ")
		if err != nil {
			return "", err
		}
		if p1 != p2 {
			status("they don't match, try again")
			continue
		}
		return p1, nil
	}
}

func cmdRemote(a []string) error {
	if !fileExists(vaultFile()) {
		return errNotInit
	}
	if len(a) == 0 {
		if u := remoteURL(); u != "" {
			fmt.Println(u)
			return nil
		}
		return errNoRemote
	}
	if err := setRemote(a[0]); err != nil {
		return err
	}
	b, err := openBackend()
	if err != nil {
		return err
	}
	defer b.Close()
	if err := b.Sync(); err != nil {
		return fmt.Errorf("remote set, but sync failed: %v", err)
	}
	status("syncing with %s", a[0])
	return nil
}

func cmdPasswd() error {
	if ag := findAgent(); ag != nil {
		ag.call("POST", "/api/lock", nil, nil)
	}
	pw, err := masterPassword("Current master password: ")
	if err != nil {
		return err
	}
	s, err := unlockLocal(pw)
	if err != nil {
		return err
	}
	s.ask = askHidden
	if hasRemote() {
		if err := s.Sync(); err != nil {
			return fmt.Errorf("sync first failed (%v) — fix that before changing the password", err)
		}
	}
	np, err := choosePassword()
	if err != nil {
		return err
	}
	if err := s.ChangePassword(np); err != nil {
		return err
	}
	if hasRemote() {
		if _, err := runGit("push", "-q", "origin", "HEAD:main"); err != nil {
			return fmt.Errorf("password changed here, but push failed: %v — run `vault sync`", err)
		}
	}
	status("master password changed. Other devices will ask for the new one on their next sync.")
	return nil
}

func cmdStatus() error {
	fmt.Println("vault      ", version)
	fmt.Println("data       ", dataDir())
	if !fileExists(vaultFile()) {
		fmt.Println("state       not set up — run `vault init`")
		return nil
	}
	if u := remoteURL(); u != "" {
		fmt.Println("sync repo  ", u)
		if out, err := runGit("log", "-1", "--format=%cr (%s)"); err == nil {
			fmt.Println("last commit", strings.TrimSpace(string(out)))
		}
	} else {
		fmt.Println("sync repo   none (local only) — `vault remote <git-url>`")
	}
	if ag := findAgent(); ag != nil {
		st, _ := ag.status()
		if st.Unlocked {
			fmt.Println("session     unlocked (agent on 127.0.0.1)")
		} else {
			fmt.Println("session     locked (agent running)")
		}
	} else {
		fmt.Println("session     locked")
	}
	return nil
}
