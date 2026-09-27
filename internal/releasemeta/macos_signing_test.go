package releasemeta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const developerIDIdentity = "Developer ID Application: KARIM SMIRES (7R2VPW8GH4)"

func TestDarwinReleaseUsesDeveloperIDNotarize(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	goreleaser := readRepoFile(t, root, ".goreleaser.yaml")
	releaseYML := readRepoFile(t, root, ".github/workflows/release.yml")

	if !strings.Contains(goreleaser, "notarize:") || !strings.Contains(goreleaser, "macos:") {
		t.Fatal(".goreleaser.yaml must declare notarize.macos for Darwin binaries")
	}
	if !strings.Contains(goreleaser, `enabled: '{{ isEnvSet "MACOS_SIGN_P12" }}'`) {
		t.Fatal(".goreleaser.yaml must enable notarize.macos only when MACOS_SIGN_P12 is set")
	}
	if !strings.Contains(goreleaser, developerIDIdentity) {
		t.Fatalf(".goreleaser.yaml must document identity %q", developerIDIdentity)
	}
	assertNoAppleDevelopmentIdentity(t, goreleaser, ".goreleaser.yaml")
	assertNoAppleDevelopmentIdentity(t, releaseYML, "release.yml")

	requiredSecrets := []string{
		"MACOS_SIGN_P12",
		"MACOS_SIGN_PASSWORD",
		"MACOS_NOTARY_KEY",
		"MACOS_NOTARY_KEY_ID",
		"MACOS_NOTARY_ISSUER_ID",
	}
	for _, name := range requiredSecrets {
		needle := "secrets." + name
		if !strings.Contains(releaseYML, needle) {
			t.Errorf("release.yml must pass %s into GoReleaser", needle)
		}
		if !strings.Contains(goreleaser, name) {
			t.Errorf(".goreleaser.yaml must reference %s", name)
		}
	}
	if !strings.Contains(releaseYML, "--timeout 60m") {
		t.Error("release.yml must raise GoReleaser timeout so notarization wait can finish")
	}
}

func TestGitignoreKeepsAppleCredentialsOutOfGit(t *testing.T) {
	t.Parallel()

	gitignore := readRepoFile(t, repoRoot(t), ".gitignore")
	for _, pattern := range []string{"*.p8", "*.p12"} {
		if !strings.Contains(gitignore, pattern) {
			t.Errorf(".gitignore must include %s", pattern)
		}
	}
}

func assertNoAppleDevelopmentIdentity(t *testing.T, contents, path string) {
	t.Helper()
	for _, line := range strings.Split(contents, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "Apple Development") {
			t.Errorf("%s must not use Apple Development as a distribution identity: %s", path, trimmed)
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
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
