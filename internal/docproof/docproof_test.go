// Package docproof checks that the review documents cite tests which exist.
//
// The v3.0.4 review listed five test names that were never written. They read
// exactly like the real ones, and a reader would have gone looking for them and
// found nothing -- which is worse than citing no proof at all, because the
// citation itself is what lends the claim its weight.
//
// A proof that cannot be run is not a proof, so the citation is checked the same
// way a test name is: it has to resolve to a function in this repository.
package docproof

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// citedTest finds backtick-quoted Go identifiers that look like test names.
var citedTest = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")

func TestReviewDocsCiteTestsThatExist(t *testing.T) {
	root := repoRoot(t)

	existing := map[string]bool{}
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range regexp.MustCompile(`func (Test[A-Za-z0-9_]+)`).FindAllStringSubmatch(string(src), -1) {
			existing[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) == 0 {
		t.Fatal("no review documents found to check")
	}

	for _, doc := range docs {
		src, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		var missing []string
		seen := map[string]bool{}
		for _, m := range citedTest.FindAllStringSubmatch(string(src), -1) {
			name := m[1]
			if seen[name] || existing[name] {
				continue
			}
			seen[name] = true
			missing = append(missing, name)
		}
		if len(missing) > 0 {
			t.Errorf("%s cites %d test(s) that do not exist: %s\n"+
				"A cited proof a reader cannot run lends the claim its weight and nothing else.",
				filepath.Base(doc), len(missing), strings.Join(missing, ", "))
		}
	}
}

// TestEveryRegisteredAdapterIsDocumented closes the other direction of drift.
//
// The first test here catches review documents citing tests that were never
// written. This one catches the reverse: an adapter that shipped without ever
// appearing in the provider table. cursor_agent went live in v3.0.5 and was
// missing from docs/PROVIDERS.md until a review pass noticed -- a table that
// lists every stub next to every OAuth adapter is what a reader trusts to be
// complete, so an omission reads as "not available" rather than "unwritten".
func TestEveryRegisteredAdapterIsDocumented(t *testing.T) {
	root := repoRoot(t)

	register, err := os.ReadFile(filepath.Join(root, "internal", "adapters", "register.go"))
	if err != nil {
		t.Fatal(err)
	}
	providers, err := os.ReadFile(filepath.Join(root, "docs", "PROVIDERS.md"))
	if err != nil {
		t.Fatal(err)
	}
	// "Documented" means a row in the table, not a mention somewhere in the
	// prose: a name can appear in a sentence and still be missing every cell a
	// reader needs (auth mode, endpoint, ToS risk). So the table's first column
	// is read, and aliases sharing a row -- `antigravity` / `gemini_oauth` --
	// each count.
	tabled := map[string]bool{}
	for _, line := range strings.Split(string(providers), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		rest := strings.TrimSpace(line)
		end := strings.Index(rest[1:], "|")
		if end < 0 {
			continue
		}
		for _, m := range regexp.MustCompile("`([a-z_0-9]+)`").FindAllStringSubmatch(rest[1:end+1], -1) {
			tabled[m[1]] = true
		}
	}
	if len(tabled) < 16 {
		t.Fatalf("only %d name(s) found in the PROVIDERS.md table; the parse has stopped working", len(tabled))
	}

	// The registered name is not always the Go identifier, and register.go is
	// the only place that knows: cursoragent.Name is "cursor_agent",
	// antigravity.AliasGemini is "gemini_oauth", kimi_oauth.NameAI is
	// "kimi_ai_oauth", and two adapters register a hosted constant in a loop.
	// So register.go is parsed for the names it passes, and each package
	// reference is resolved to the constant's value.
	names := append(literalRegistrations(t, register), hostedSpecNames(t, filepath.Join(root, "internal", "adapter", "hosted"))...)
	names = append(names, resolvedRegistrations(t, root, register)...)
	if len(names) == 0 {
		t.Fatal("no adapter names were resolved from register.go; this test has stopped checking anything")
	}

	var missing []string
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if !tabled[name] {
			missing = append(missing, name)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no adapters found in register.go; this test has stopped checking anything")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("docs/PROVIDERS.md does not document %d registered adapter(s): %s\n"+
			"Every other adapter has a row, including the stubs that exist only to say "+
			"\"there is no such thing\", so an omission reads as unavailability rather than "+
			"as an unwritten entry.",
			len(missing), strings.Join(missing, ", "))
	}
}

// hostedSpecNames reads the adapter names out of the data-driven hosted specs.
func hostedSpecNames(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// The spec struct's field is `Name:`, not `Adapter:` -- reading the
		// wrong one silently returned an empty set, which left every hosted
		// adapter (groq, workers_ai, cerebras, ...) outside the check.
		for _, m := range regexp.MustCompile(`\bName:\s*"([a-z_0-9]+)"`).FindAllStringSubmatch(string(src), -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// resolvedRegistrations reads register.go for `r.Register(pkg.Ident, ...)` and
// resolves each Ident to the string its package declares. A reference that is
// not a package -- a loop variable over the hosted specs -- is skipped; the
// hosted scan covers those.
func resolvedRegistrations(t *testing.T, root string, register []byte) []string {
	t.Helper()
	var out []string
	for _, m := range regexp.MustCompile(`Register\(\s*([a-z][a-z0-9_]*)\.([A-Za-z0-9_]+)\s*,`).FindAllStringSubmatch(string(register), -1) {
		pkg, ident := m[1], m[2]
		dir := filepath.Join(root, "internal", "adapter", pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			src, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			re := regexp.MustCompile(`\b` + ident + `\s*=\s*"([a-z_0-9]+)"`)
			if v := re.FindSubmatch(src); v != nil {
				out = append(out, string(v[1]))
				break
			}
		}
	}
	return out
}

// literalRegistrations reads registrations made with a bare string rather than a
// constant, which the resolver above cannot see.
func literalRegistrations(t *testing.T, register []byte) []string {
	t.Helper()
	var out []string
	for _, m := range regexp.MustCompile(`Register\(\s*"([a-z_0-9]+)"`).FindAllStringSubmatch(string(register), -1) {
		out = append(out, m[1])
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod above the working directory")
		}
		dir = parent
	}
}
