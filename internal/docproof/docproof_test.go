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
	var names []string
	providers, err := os.ReadFile(filepath.Join(root, "docs", "PROVIDERS.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(providers)

	// The name is not always the Go identifier: cursoragent.Name is
	// "cursor_agent", and two adapters register a hosted constant. So the
	// constant's value is read from the adapter package, which is what the
	// registry actually looks up.
	adaptersDir := filepath.Join(root, "internal", "adapter")
	entries, err := os.ReadDir(adaptersDir)
	if err != nil {
		t.Fatal(err)
	}
	// specs are the data-driven hosted wrappers, registered in a loop.
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "hosted" {
			continue
		}
		name := declaredAdapterName(t, filepath.Join(adaptersDir, e.Name()))
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	names = append(names, hostedSpecNames(t, filepath.Join(adaptersDir, "hosted"))...)
	if len(register) == 0 {
		t.Fatal("internal/adapters/register.go is empty; this test has stopped checking anything")
	}

	var missing []string
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if !strings.Contains(doc, "`"+name+"`") {
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

// declaredAdapterName reads the value an adapter package registers under.
func declaredAdapterName(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if m := regexp.MustCompile(`Name\s*=\s*"([a-z_0-9]+)"`).FindSubmatch(src); m != nil {
			return string(m[1])
		}
	}
	return ""
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
		for _, m := range regexp.MustCompile(`Adapter:\s*"([a-z_0-9]+)"`).FindAllStringSubmatch(string(src), -1) {
			out = append(out, m[1])
		}
		for _, m := range regexp.MustCompile(`Register\(\s*"([a-z_0-9]+)"`).FindAllStringSubmatch(string(src), -1) {
			out = append(out, m[1])
		}
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
