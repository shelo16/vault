package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Field is one named value inside an entry, e.g. username, password, url, cmd, note.
type Field struct {
	Key    string `json:"k"`
	Value  string `json:"v"`
	Secret bool   `json:"s,omitempty"`
}

// Entry is a named record such as "acme/prod-db". Deleted entries are kept
// as tombstones so deletions sync correctly between devices.
type Entry struct {
	Fields  []Field `json:"f,omitempty"`
	Updated int64   `json:"u"`
	Deleted bool    `json:"d,omitempty"`
}

type Vault struct {
	Entries map[string]*Entry `json:"entries"`
}

// Change is one edit: replace an entry's fields, or delete it.
type Change struct {
	Path   string  `json:"path"`
	Fields []Field `json:"fields,omitempty"`
	Delete bool    `json:"delete,omitempty"`
}

func newVault() *Vault { return &Vault{Entries: map[string]*Entry{}} }

func cloneVault(v *Vault) *Vault {
	c := newVault()
	c.Merge(v)
	return c
}

// cleanPath normalises "  globex / build-console/" to "globex/build-console".
func cleanPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "/")
}

// Paths returns all live entry paths, sorted case-insensitively.
func (v *Vault) Paths() []string {
	var out []string
	for p, e := range v.Entries {
		if !e.Deleted {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// Lookup finds a live entry by exact path (case-insensitive).
func (v *Vault) Lookup(path string) (string, *Entry) {
	path = cleanPath(path)
	if e, ok := v.Entries[path]; ok && !e.Deleted {
		return path, e
	}
	lp := strings.ToLower(path)
	for p, e := range v.Entries {
		if !e.Deleted && strings.ToLower(p) == lp {
			return p, e
		}
	}
	return "", nil
}

func stamp(prev int64) int64 {
	n := time.Now().UnixNano()
	if n <= prev {
		n = prev + 1
	}
	return n
}

func (v *Vault) Put(path string, fields []Field) {
	path = cleanPath(path)
	var prev int64
	if old, e := v.Lookup(path); e != nil && old != path {
		v.Entries[old] = &Entry{Updated: stamp(e.Updated), Deleted: true}
	}
	if e, ok := v.Entries[path]; ok {
		prev = e.Updated
	}
	v.Entries[path] = &Entry{Fields: append([]Field(nil), fields...), Updated: stamp(prev)}
}

func (v *Vault) Delete(path string) bool {
	p, e := v.Lookup(path)
	if e == nil {
		return false
	}
	v.Entries[p] = &Entry{Updated: stamp(e.Updated), Deleted: true}
	return true
}

func (v *Vault) Apply(chs []Change) {
	for _, c := range chs {
		if c.Delete {
			v.Delete(c.Path)
		} else {
			v.Put(c.Path, c.Fields)
		}
	}
}

// Merge pulls in every entry from o that is newer than ours. Returns true if anything changed.
func (v *Vault) Merge(o *Vault) bool {
	changed := false
	for p, e := range o.Entries {
		cur, ok := v.Entries[p]
		if !ok || e.Updated > cur.Updated {
			cp := *e
			cp.Fields = append([]Field(nil), e.Fields...)
			v.Entries[p] = &cp
			changed = true
		}
	}
	return changed
}

// Under returns live paths equal to prefix or nested below it. Empty prefix = everything.
func (v *Vault) Under(prefix string) []string {
	prefix = strings.ToLower(cleanPath(prefix))
	var out []string
	for _, p := range v.Paths() {
		lp := strings.ToLower(p)
		if prefix == "" || lp == prefix || strings.HasPrefix(lp, prefix+"/") {
			out = append(out, p)
		}
	}
	return out
}

func fieldsEqual(a, b []Field) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// looksSecret decides which fields are masked by default.
func looksSecret(key string) bool {
	k := strings.ToLower(key)
	for _, w := range []string{"pass", "pwd", "secret", "token", "apikey", "api-key", "api_key", "private"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

// norm keeps only lowercase letters and digits, so "globex-build-console",
// "globex.build console" and "globex/build-console" all compare equal.
func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func tokens(q string) []string {
	var out []string
	for _, t := range strings.Fields(q) {
		if n := norm(t); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func containsAll(s string, toks []string) bool {
	for _, t := range toks {
		if !strings.Contains(s, t) {
			return false
		}
	}
	return true
}

func fieldIndex(e *Entry, key string) int {
	for i, f := range e.Fields {
		if strings.EqualFold(f.Key, key) {
			return i
		}
	}
	return -1
}

// Hit points at an entry (Field = -1) or at one field of it.
type Hit struct {
	Path  string
	Field int
}

type ResultKind int

const (
	NoMatch ResultKind = iota
	FieldMatch
	EntryMatch
	ListMatch
)

type Result struct {
	Kind ResultKind
	Hit  Hit
	List []Hit
}

func entryHits(paths []string) []Hit {
	out := make([]Hit, len(paths))
	for i, p := range paths {
		out[i] = Hit{p, -1}
	}
	return out
}

// Resolve turns what the user typed into an entry, a field, or a list to pick from.
//
//	acme                    -> list of everything under acme/
//	acme/prod-db            -> the entry
//	acme.prod-db.password   -> that field
//	globex-build-console        -> fuzzy: punctuation is ignored
//	prod db pass                -> fuzzy: every word must appear
func (v *Vault) Resolve(q string) Result {
	q = strings.TrimSpace(q)
	if q == "" {
		return Result{Kind: ListMatch, List: entryHits(v.Paths())}
	}
	for _, c := range []string{q, strings.ReplaceAll(q, ".", "/")} {
		c = cleanPath(c)
		if c == "" {
			continue
		}
		if p, e := v.Lookup(c); e != nil {
			return Result{Kind: EntryMatch, Hit: Hit{p, -1}}
		}
		if i := strings.LastIndex(c, "/"); i > 0 {
			if p, e := v.Lookup(c[:i]); e != nil {
				if fi := fieldIndex(e, c[i+1:]); fi >= 0 {
					return Result{Kind: FieldMatch, Hit: Hit{p, fi}}
				}
			}
		}
		lc := strings.ToLower(c)
		var g []string
		for _, p := range v.Paths() {
			if strings.HasPrefix(strings.ToLower(p), lc+"/") {
				g = append(g, p)
			}
		}
		if len(g) > 0 {
			return Result{Kind: ListMatch, List: entryHits(g)}
		}
	}

	toks := tokens(q)
	if len(toks) == 0 {
		return Result{Kind: NoMatch}
	}
	nq := norm(q)
	var ents []Hit
	for _, p := range v.Paths() {
		np := norm(p)
		if np == nq {
			return Result{Kind: EntryMatch, Hit: Hit{p, -1}}
		}
		if containsAll(np, toks) {
			ents = append(ents, Hit{p, -1})
		}
	}
	if len(ents) == 1 {
		return Result{Kind: EntryMatch, Hit: ents[0]}
	}
	if len(ents) > 1 {
		return Result{Kind: ListMatch, List: ents}
	}
	var fields []Hit
	for _, p := range v.Paths() {
		e := v.Entries[p]
		for i, f := range e.Fields {
			t := norm(p + "/" + f.Key)
			if t == nq {
				return Result{Kind: FieldMatch, Hit: Hit{p, i}}
			}
			if containsAll(t, toks) {
				fields = append(fields, Hit{p, i})
			}
		}
	}
	switch len(fields) {
	case 0:
		return Result{Kind: NoMatch}
	case 1:
		return Result{Kind: FieldMatch, Hit: fields[0]}
	}
	return Result{Kind: ListMatch, List: fields}
}

// Find searches paths, keys and non-secret values (e.g. an IP address).
func (v *Vault) Find(q string) []Hit {
	toks := tokens(q)
	if len(toks) == 0 {
		return nil
	}
	var out []Hit
	for _, p := range v.Paths() {
		for i, f := range v.Entries[p].Fields {
			hay := norm(p + "/" + f.Key)
			if !f.Secret {
				hay += norm(f.Value)
			}
			if containsAll(hay, toks) {
				out = append(out, Hit{p, i})
			}
		}
	}
	return out
}

// ---- Plain-text format used by import, export, add and edit ----
//
//	# comment
//	[acme/prod-db]
//	username = APPUSER
//	password = ...           (keys with pass/secret/token are secret automatically)
//	!pin = 1234              (a leading ! marks any key secret)
//	note = """
//	multi-line text
//	"""

type Section struct {
	Path   string
	Fields []Field
}

func parseText(s string) ([]Section, error) {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var secs []Section
	cur := -1
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			p := cleanPath(t[1 : len(t)-1])
			if p == "" {
				return nil, fmt.Errorf("line %d: empty [section] name", i+1)
			}
			secs = append(secs, Section{Path: p})
			cur = len(secs) - 1
			continue
		}
		if cur < 0 {
			return nil, fmt.Errorf("line %d: %q is outside of a [section]", i+1, t)
		}
		eq := strings.Index(t, "=")
		if eq < 0 {
			return nil, fmt.Errorf("line %d: expected `key = value`, got %q", i+1, t)
		}
		key := strings.TrimSpace(t[:eq])
		val := strings.TrimSpace(t[eq+1:])
		secret := false
		if strings.HasPrefix(key, "!") {
			secret = true
			key = strings.TrimSpace(key[1:])
		}
		if key == "" {
			return nil, fmt.Errorf("line %d: missing key before =", i+1)
		}
		if val == `"""` {
			start := i + 1
			var block []string
			closed := false
			for i++; i < len(lines); i++ {
				if strings.TrimSpace(lines[i]) == `"""` {
					closed = true
					break
				}
				block = append(block, lines[i])
			}
			if !closed {
				return nil, fmt.Errorf("line %d: multi-line value for %q is missing its closing \"\"\"", start, key)
			}
			val = strings.Join(block, "\n")
		}
		if looksSecret(key) {
			secret = true
		}
		secs[cur].Fields = append(secs[cur].Fields, Field{Key: key, Value: val, Secret: secret})
	}
	// merge repeated sections
	var out []Section
	idx := map[string]int{}
	for _, s := range secs {
		k := strings.ToLower(s.Path)
		if j, ok := idx[k]; ok {
			out[j].Fields = append(out[j].Fields, s.Fields...)
			continue
		}
		idx[k] = len(out)
		out = append(out, s)
	}
	return out, nil
}

func renderText(v *Vault, paths []string) string {
	var b strings.Builder
	for i, p := range paths {
		_, e := v.Lookup(p)
		if e == nil {
			continue
		}
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "[%s]\n", p)
		for _, f := range e.Fields {
			k := f.Key
			if f.Secret && !looksSecret(k) {
				k = "!" + k
			}
			if strings.Contains(f.Value, "\n") {
				fmt.Fprintf(&b, "%s = \"\"\"\n%s\n\"\"\"\n", k, f.Value)
			} else {
				fmt.Fprintf(&b, "%s = %s\n", k, f.Value)
			}
		}
	}
	return b.String()
}
