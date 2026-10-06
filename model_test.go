package main

import (
	"strings"
	"testing"
)

const sample = `
# work
[acme/prod-db]
username = APPUSER
password = p@ss=word!
url = jdbc:sqlserver://10.0.0.1;database=APP;encrypt=true;

[acme/staging-test]
ssh = root@10.0.0.12
password = x

[globex/build-console]
cmd = mvn clean install -Pmc,cp,globex -Dmaven.test.skip=true

[globex/build-portal]
cmd = mvn clean install -Pcp,globex -Dmaven.test.skip=true

[globex/deploy-prod]
note = """
backend: API
  * nvm use 20
"""

[personal/mac]
user = me
!apple-id = secret-ish
`

func load(t *testing.T) *Vault {
	t.Helper()
	secs, err := parseText(sample)
	if err != nil {
		t.Fatal(err)
	}
	v := newVault()
	for _, s := range secs {
		v.Put(s.Path, s.Fields)
	}
	return v
}

func TestParse(t *testing.T) {
	v := load(t)
	if n := len(v.Paths()); n != 6 {
		t.Fatalf("want 6 entries, got %d", n)
	}
	_, e := v.Lookup("acme/prod-db")
	if e.Fields[1].Value != "p@ss=word!" || !e.Fields[1].Secret {
		t.Fatalf("password parse: %+v", e.Fields[1])
	}
	if !strings.HasSuffix(e.Fields[2].Value, "encrypt=true;") {
		t.Fatalf("url: %q", e.Fields[2].Value)
	}
	_, e = v.Lookup("globex/deploy-prod")
	if e.Fields[0].Value != "backend: API\n  * nvm use 20" {
		t.Fatalf("multiline: %q", e.Fields[0].Value)
	}
	_, e = v.Lookup("personal/mac")
	if !e.Fields[1].Secret || e.Fields[0].Secret {
		t.Fatal("! secret marker")
	}
}

func TestRoundTrip(t *testing.T) {
	v := load(t)
	text := renderText(v, v.Paths())
	secs, err := parseText(text)
	if err != nil {
		t.Fatal(err)
	}
	if d := diffSections(v, v.Paths(), secs); len(d) != 0 {
		t.Fatalf("round trip changed things: %+v", d)
	}
}

func TestResolve(t *testing.T) {
	v := load(t)
	cases := []struct {
		q    string
		kind ResultKind
		path string
		key  string
	}{
		{"acme", ListMatch, "", ""},
		{"acme/prod-db", EntryMatch, "acme/prod-db", ""},
		{"ACME/PROD-DB", EntryMatch, "acme/prod-db", ""},
		{"acme.prod-db.password", FieldMatch, "acme/prod-db", "password"},
		{"acme/prod-db/url", FieldMatch, "acme/prod-db", "url"},
		{"globex-build-console", EntryMatch, "globex/build-console", ""},
		{"globex build", ListMatch, "", ""},
		{"prod db pass", FieldMatch, "acme/prod-db", "password"},
		{"staging ssh", FieldMatch, "acme/staging-test", "ssh"},
		{"nothing-here", NoMatch, "", ""},
	}
	for _, c := range cases {
		r := v.Resolve(c.q)
		if r.Kind != c.kind {
			t.Errorf("%q: kind %v, want %v (%+v)", c.q, r.Kind, c.kind, r)
			continue
		}
		if c.path != "" && r.Hit.Path != c.path {
			t.Errorf("%q: path %q want %q", c.q, r.Hit.Path, c.path)
		}
		if c.key != "" && v.Entries[r.Hit.Path].Fields[r.Hit.Field].Key != c.key {
			t.Errorf("%q: wrong field", c.q)
		}
	}
	if hits := v.Find("10.0.0.12"); len(hits) != 1 {
		t.Errorf("find by IP: %v", hits)
	}
	if hits := v.Find("p@ss"); len(hits) != 0 {
		t.Errorf("find must not search secret values: %v", hits)
	}
}

func TestMerge(t *testing.T) {
	a := load(t)
	b := cloneVault(a)
	a.Put("globex/build-console", []Field{{Key: "cmd", Value: "edited on A"}})
	b.Put("acme/new-thing", []Field{{Key: "url", Value: "x"}})
	b.Delete("personal/mac")
	a.Merge(b)
	b.Merge(a)
	for _, v := range []*Vault{a, b} {
		if _, e := v.Lookup("globex/build-console"); e.Fields[0].Value != "edited on A" {
			t.Fatal("edit lost")
		}
		if _, e := v.Lookup("acme/new-thing"); e == nil {
			t.Fatal("add lost")
		}
		if _, e := v.Lookup("personal/mac"); e != nil {
			t.Fatal("delete lost")
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"key = v", "[a]\nnoequals", "[a]\nk = \"\"\"\nunclosed", "[ ]\nk=v"} {
		if _, err := parseText(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestCrypto(t *testing.T) {
	s, err := newSession("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	s.V = load(t)
	data, err := s.seal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "APPUSER") {
		t.Fatal("plaintext leaked")
	}
	if _, _, _, err := (&Session{}).decrypt(data, "wrong"); err != errBadPassword {
		t.Fatalf("want bad password, got %v", err)
	}
	v, _, _, err := (&Session{}).decrypt(data, "correct horse battery")
	if err != nil || len(v.Paths()) != 6 {
		t.Fatalf("decrypt: %v", err)
	}
}
