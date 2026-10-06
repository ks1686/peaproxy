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
