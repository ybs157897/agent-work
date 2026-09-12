// Package knowledgefixtures verifies the synthetic multi-repository Java fixture
// produced by generate.sh.
//
// The fixture backs the unified project knowledge library tests: it gives the
// knowledge agent three independent git repositories with a genuine cross-service
// relationship (order-service publishes an event declared in the shared common library
// that device-service consumes), a second commit for real history, and a dirty working
// tree so frozen dirty input is exercised.
package knowledgefixtures

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const (
	// generateScript is executed with the package directory as its working directory.
	generateScript = "generate.sh"

	// fixedGeneratedAt is the generator's fixed manifest timestamp. It must never be
	// "now", otherwise two runs could not produce the same manifest.
	fixedGeneratedAt = "2026-01-02T03:04:05+00:00"

	// rootPlaceholder stands in for the absolute fixture root when two manifests are
	// compared byte for byte. The manifest records each repository's absolute path
	// (required by the fixture contract), so manifests generated into two different
	// directories can only be identical once that one environment-derived value is
	// normalised away. Everything else must match byte for byte.
	rootPlaceholder = "<FIXTURE_ROOT>"
)

// repoEntry mirrors one element of the manifest's "repos" array.
type repoEntry struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Path        string   `json:"path"`
	Branch      string   `json:"branch"`
	HeadCommit  string   `json:"head_commit"`
	CommitCount int      `json:"commit_count"`
	Dirty       bool     `json:"dirty"`
	Untracked   []string `json:"untracked"`
	Artifact    string   `json:"artifact"`
	Consumer    string   `json:"consumer"`
}

// manifest mirrors fixture-manifest.json.
type manifest struct {
	GeneratedAt string      `json:"generated_at"`
	Notes       string      `json:"notes"`
	Repos       []repoEntry `json:"repos"`
}

// pomArtifactVersionRe matches an <artifactId> immediately followed by its <version>,
// which is the shape of a Maven dependency (and of a project's own coordinates).
var pomArtifactVersionRe = regexp.MustCompile(
	`<artifactId>\s*([^<]+?)\s*</artifactId>\s*<version>\s*([^<]+?)\s*</version>`)

func TestGeneratedFixture(t *testing.T) {
	requireGit(t)
	pkgDir := packageDir(t)

	firstDir := t.TempDir()
	runGenerator(t, pkgDir, firstDir)
	first := loadManifest(t, firstDir)

	t.Run("three independent git repositories", func(t *testing.T) {
		for _, name := range []string{"order-service", "device-service", "common"} {
			entry := repoByName(t, first, name)

			info, err := os.Stat(entry.Path)
			if err != nil || !info.IsDir() {
				t.Fatalf("repository %s is not a directory at %s (err=%v)", name, entry.Path, err)
			}
			if _, err := os.Stat(filepath.Join(entry.Path, ".git")); err != nil {
				t.Fatalf("repository %s has no .git at %s: %v", name, entry.Path, err)
			}
			if got := gitLine(t, entry.Path, "rev-parse", "--is-inside-work-tree"); got != "true" {
				t.Fatalf("git rev-parse --is-inside-work-tree in %s = %q, want %q", name, got, "true")
			}
			if got := gitLine(t, entry.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
				t.Fatalf("HEAD branch of %s = %q, want %q", name, got, "main")
			}
			wantPath := filepath.Join(firstDir, name)
			if !filepath.IsAbs(entry.Path) || entry.Path != wantPath {
				t.Fatalf("manifest path for %s = %q, want absolute path %q", name, entry.Path, wantPath)
			}
		}
	})

	t.Run("recorded head commits match git", func(t *testing.T) {
		for _, entry := range first.Repos {
			got := gitLine(t, entry.Path, "rev-parse", "HEAD")
			if got != entry.HeadCommit {
				t.Fatalf("manifest head_commit for %s = %s but git rev-parse HEAD = %s", entry.Name, entry.HeadCommit, got)
			}
			if got := gitLine(t, entry.Path, "rev-list", "--count", "HEAD"); got != fmt.Sprint(entry.CommitCount) {
				t.Fatalf("manifest commit_count for %s = %d but git rev-list --count HEAD = %s", entry.Name, entry.CommitCount, got)
			}
		}
		if got := repoByName(t, first, "order-service").CommitCount; got != 2 {
			t.Fatalf("order-service should carry real history: commit_count = %d, want 2", got)
		}
		for _, name := range []string{"device-service", "common"} {
			if got := repoByName(t, first, name).CommitCount; got != 1 {
				t.Fatalf("%s should have a single commit: commit_count = %d, want 1", name, got)
			}
		}
	})

	t.Run("working tree cleanliness", func(t *testing.T) {
		orderRepo := repoByName(t, first, "order-service").Path
		if status := gitLines(t, orderRepo, "status", "--porcelain"); len(status) != 0 {
			t.Fatalf("order-service must be clean, got status:\n%s", strings.Join(status, "\n"))
		}
		if status := gitLines(t, repoByName(t, first, "common").Path, "status", "--porcelain"); len(status) != 0 {
			t.Fatalf("common must be clean, got status:\n%s", strings.Join(status, "\n"))
		}

		deviceEntry := repoByName(t, first, "device-service")
		deviceRepo := deviceEntry.Path
		status := gitLines(t, deviceRepo, "status", "--porcelain")
		if len(status) == 0 {
			t.Fatalf("device-service must be dirty, but git status --porcelain is empty")
		}
		if !deviceEntry.Dirty {
			t.Fatalf("manifest marks device-service dirty=false, want true")
		}

		var untracked []string
		var modified []string
		for _, line := range status {
			switch {
			case strings.HasPrefix(line, "?? "):
				untracked = append(untracked, strings.TrimSpace(strings.TrimPrefix(line, "?? ")))
			case strings.HasPrefix(line, " M"), strings.HasPrefix(line, "M "):
				modified = append(modified, strings.TrimSpace(line[2:]))
			}
		}
		if len(untracked) != 1 {
			t.Fatalf("device-service must have exactly one untracked file, got %d: %v", len(untracked), untracked)
		}
		if !strings.HasSuffix(untracked[0], "ReleaseRetryPolicy.java") {
			t.Fatalf("untracked file = %q, want the ReleaseRetryPolicy.java fixture file", untracked[0])
		}
		if len(modified) != 1 || !strings.HasSuffix(modified[0], "DeviceReservationService.java") {
			t.Fatalf("device-service must have exactly one modified tracked file (DeviceReservationService.java), got %v", modified)
		}
		if len(deviceEntry.Untracked) != 1 || deviceEntry.Untracked[0] != untracked[0] {
			t.Fatalf("manifest untracked for device-service = %v, git reports %v", deviceEntry.Untracked, untracked)
		}
	})

	t.Run("common is consumed at two different versions", func(t *testing.T) {
		orderVersion := pomArtifactVersion(t, filepath.Join(repoByName(t, first, "order-service").Path, "pom.xml"), "common")
		deviceVersion := pomArtifactVersion(t, filepath.Join(repoByName(t, first, "device-service").Path, "pom.xml"), "common")
		commonEntry := repoByName(t, first, "common")
		commonVersion := pomArtifactVersion(t, filepath.Join(commonEntry.Path, "pom.xml"), "common")

		if orderVersion == deviceVersion {
			t.Fatalf("order-service and device-service both depend on common %s; the fixture needs deliberate version skew", orderVersion)
		}
		if commonVersion != orderVersion {
			t.Fatalf("common's own artifact version = %s, want the order-service version %s", commonVersion, orderVersion)
		}
		if commonEntry.Kind != "common" {
			t.Fatalf("manifest kind for common = %q, want %q", commonEntry.Kind, "common")
		}
		if got := commonEntry.Artifact; got != "com.example:common:"+commonVersion {
			t.Fatalf("manifest artifact for common = %q, want %q", got, "com.example:common:"+commonVersion)
		}
		if !strings.Contains(commonEntry.Consumer, orderVersion) || !strings.Contains(commonEntry.Consumer, deviceVersion) {
			t.Fatalf("manifest consumer note for common = %q, want it to name both %s and %s", commonEntry.Consumer, orderVersion, deviceVersion)
		}
		for name, version := range map[string]string{"order-service": orderVersion, "device-service": deviceVersion} {
			if consumer := repoByName(t, first, name).Consumer; !strings.Contains(consumer, version) {
				t.Fatalf("manifest consumer note for %s = %q, want it to name common %s", name, consumer, version)
			}
		}
	})

	t.Run("cross-service event link is discoverable from source text", func(t *testing.T) {
		orderFiles := readTree(t, repoByName(t, first, "order-service").Path)
		deviceFiles := readTree(t, repoByName(t, first, "device-service").Path)

		publisher := findFile(orderFiles, func(name, body string) bool {
			return strings.HasSuffix(name, ".java") &&
				strings.Contains(body, "OrderCancelledEvent") &&
				strings.Contains(body, "publish(")
		})
		if publisher == "" {
			t.Fatalf("no order-service source publishes OrderCancelledEvent; files: %v", sortedKeys(orderFiles))
		}

		endpoint := findFile(orderFiles, func(name, body string) bool {
			return strings.Contains(body, "/orders/{id}/cancel") && strings.Contains(body, "PostMapping")
		})
		if endpoint == "" {
			t.Fatalf("no order-service source exposes POST /orders/{id}/cancel; files: %v", sortedKeys(orderFiles))
		}

		handler := findFile(deviceFiles, func(name, body string) bool {
			return strings.HasSuffix(name, ".java") &&
				strings.Contains(body, "onOrderCancelled") &&
				strings.Contains(body, "OrderCancelledEvent") &&
				strings.Contains(body, "releaseReservation")
		})
		if handler == "" {
			t.Fatalf("no device-service source handles OrderCancelledEvent via releaseReservation; files: %v", sortedKeys(deviceFiles))
		}

		releaser := findFile(deviceFiles, func(name, body string) bool {
			return strings.Contains(body, "releaseReservation") && strings.Contains(body, "RELEASE_RETRY_ATTEMPTS")
		})
		if releaser == "" {
			t.Fatalf("no device-service source pairs releaseReservation with the retry constant; files: %v", sortedKeys(deviceFiles))
		}

		// The topic literal must exist on both sides of the link.
		const topic = "order.cancelled"
		for name, files := range map[string]map[string]string{"order-service": orderFiles, "device-service": deviceFiles} {
			if findFile(files, func(_, body string) bool { return strings.Contains(body, topic) }) == "" {
				t.Fatalf("%s never mentions the shared topic %q", name, topic)
			}
		}

		// order-service is a producer only: no listener may be declared there.
		if found := findFile(orderFiles, func(_, body string) bool { return strings.Contains(body, "@KafkaListener") }); found != "" {
			t.Fatalf("order-service must not declare a message listener, found one in %s", found)
		}
		if got := repoByName(t, first, "common").Kind; got != "common" {
			t.Fatalf("common kind = %q, want %q", got, "common")
		}
	})

	t.Run("manifest header is fixed synthetic material", func(t *testing.T) {
		if first.GeneratedAt != fixedGeneratedAt {
			t.Fatalf("manifest generated_at = %q, want the fixed string %q", first.GeneratedAt, fixedGeneratedAt)
		}
		if !strings.Contains(first.Notes, "Synthetic fixture material") || !strings.Contains(first.Notes, "not real project data") {
			t.Fatalf("manifest notes must state this is synthetic fixture material, got %q", first.Notes)
		}
	})

	t.Run("generation is deterministic", func(t *testing.T) {
		secondDir := t.TempDir()
		runGenerator(t, pkgDir, secondDir)
		second := loadManifest(t, secondDir)

		// Commit SHAs are the real determinism proof: identical inputs must produce
		// identical objects, so a mismatch here means the fixture history is not
		// reproducible (ambient git config, timestamps or identity leaked in).
		var mismatches []string
		for _, entry := range first.Repos {
			other := repoByName(t, second, entry.Name)
			if entry.HeadCommit != other.HeadCommit {
				mismatches = append(mismatches, fmt.Sprintf(
					"  %s: run1 %s (%s) != run2 %s (%s)",
					entry.Name, entry.HeadCommit, entry.Path, other.HeadCommit, other.Path))
			}
		}
		if len(mismatches) > 0 {
			t.Fatalf("commit SHAs are NOT reproducible across runs:\n%s", strings.Join(mismatches, "\n"))
		}

		firstRaw := readFile(t, filepath.Join(firstDir, "fixture-manifest.json"))
		secondRaw := readFile(t, filepath.Join(secondDir, "fixture-manifest.json"))

		// Each run must record its own absolute repository paths.
		for _, pair := range []struct {
			raw  []byte
			root string
		}{{firstRaw, firstDir}, {secondRaw, secondDir}} {
			if !bytes.Contains(pair.raw, []byte(pair.root)) {
				t.Fatalf("manifest in %s does not contain its own absolute fixture root", pair.root)
			}
		}
		if bytes.Equal(firstRaw, secondRaw) {
			t.Fatalf("manifests from two different directories are identical, which cannot be true while path is absolute")
		}

		firstNorm := normalizeRoot(firstRaw, firstDir)
		secondNorm := normalizeRoot(secondRaw, secondDir)
		if !bytes.Equal(firstNorm, secondNorm) {
			t.Fatalf("manifests are not identical after normalising the fixture root:\n%s", firstDiff(firstNorm, secondNorm))
		}
	})
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available on PATH; skipping the synthetic fixture test")
	}
}

// packageDir returns the directory holding generate.sh. `go test` runs with the
// package directory as the working directory, and runtime.Caller is the fallback.
func packageDir(t *testing.T) string {
	t.Helper()
	if wd, err := os.Getwd(); err == nil {
		if _, statErr := os.Stat(filepath.Join(wd, generateScript)); statErr == nil {
			return wd
		}
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		dir := filepath.Dir(file)
		if _, err := os.Stat(filepath.Join(dir, generateScript)); err == nil {
			return dir
		}
	}
	t.Fatalf("cannot locate %s next to the test file", generateScript)
	return ""
}

func runGenerator(t *testing.T, pkgDir, target string) {
	t.Helper()
	cmd := exec.Command("bash", generateScript, target)
	cmd.Dir = pkgDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash %s %s failed: %v\n%s", generateScript, target, err, out)
	}
}

func gitLine(t *testing.T, repo string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(gitOutput(t, repo, args...))
}

func gitLines(t *testing.T, repo string, args ...string) []string {
	t.Helper()
	out := strings.TrimRight(gitOutput(t, repo, args...), "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", full...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s in %s failed: %v", strings.Join(args, " "), repo, err)
	}
	return string(out)
}

func loadManifest(t *testing.T, dir string) manifest {
	t.Helper()
	raw := readFile(t, filepath.Join(dir, "fixture-manifest.json"))
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parsing fixture-manifest.json in %s: %v", dir, err)
	}
	if len(m.Repos) == 0 {
		t.Fatalf("fixture-manifest.json in %s lists no repositories", dir)
	}
	return m
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

func repoByName(t *testing.T, m manifest, name string) repoEntry {
	t.Helper()
	for _, entry := range m.Repos {
		if entry.Name == name {
			return entry
		}
	}
	names := make([]string, 0, len(m.Repos))
	for _, entry := range m.Repos {
		names = append(names, entry.Name)
	}
	t.Fatalf("manifest has no repository named %q (has %v)", name, names)
	return repoEntry{}
}

// readTree returns every file below root keyed by its slash-separated relative path,
// skipping git's own object store.
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return files
}

// pomArtifactVersion returns the <version> that immediately follows
// <artifactId>artifactID</artifactId> in the given pom.
func pomArtifactVersion(t *testing.T, pomPath, artifactID string) string {
	t.Helper()
	body := string(readFile(t, pomPath))
	for _, match := range pomArtifactVersionRe.FindAllStringSubmatch(body, -1) {
		if strings.TrimSpace(match[1]) == artifactID {
			return strings.TrimSpace(match[2])
		}
	}
	t.Fatalf("%s declares no <artifactId>%s</artifactId> followed by a <version>", pomPath, artifactID)
	return ""
}

func findFile(files map[string]string, match func(name, body string) bool) string {
	for _, name := range sortedKeys(files) {
		if match(name, files[name]) {
			return name
		}
	}
	return ""
}

func sortedKeys(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func normalizeRoot(manifestBytes []byte, root string) []byte {
	return bytes.ReplaceAll(manifestBytes, []byte(root), []byte(rootPlaceholder))
}

// firstDiff renders the first differing line of two documents, for failure output.
func firstDiff(a, b []byte) string {
	aLines := strings.Split(string(a), "\n")
	bLines := strings.Split(string(b), "\n")
	for i := 0; i < len(aLines) || i < len(bLines); i++ {
		var left, right string
		if i < len(aLines) {
			left = aLines[i]
		}
		if i < len(bLines) {
			right = bLines[i]
		}
		if left != right {
			return fmt.Sprintf("line %d:\n  run1: %s\n  run2: %s", i+1, left, right)
		}
	}
	return "no differing line found"
}
