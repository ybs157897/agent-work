package knowledgelib

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Zone directories inside a library root.
const (
	ContentZone     = "content"
	CatalogZone     = "catalog"
	SourcesZone     = "_sources"
	SystemZone      = "_system"
	StagingZone     = "_system/staging"
	ReleasesZone    = "_system/releases"
	ReprZone        = "_system/representations"
	LegacyZone      = "_system/legacy-import"
	ManifestName    = "manifest.yaml"
	SourcesFileName = "manifest.yaml"
)

// Library owns the on-disk layout of one unified knowledge library.
type Library struct {
	Root string
}

func NewLibrary(root string) (Library, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return Library{}, fmt.Errorf("%w: knowledge library root is required", ErrValidation)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Library{}, fmt.Errorf("%w: resolve library root: %v", ErrValidation, err)
	}
	return Library{Root: abs}, nil
}

func (l Library) ContentDir() string  { return filepath.Join(l.Root, ContentZone) }
func (l Library) CatalogDir() string  { return filepath.Join(l.Root, CatalogZone) }
func (l Library) SourcesDir() string  { return filepath.Join(l.Root, SourcesZone) }
func (l Library) SystemDir() string   { return filepath.Join(l.Root, SystemZone) }
func (l Library) StagingRoot() string { return filepath.Join(l.Root, filepath.FromSlash(StagingZone)) }
func (l Library) ReleasesDir() string { return filepath.Join(l.Root, filepath.FromSlash(ReleasesZone)) }
func (l Library) ReprDir() string     { return filepath.Join(l.Root, filepath.FromSlash(ReprZone)) }
func (l Library) LegacyDir() string   { return filepath.Join(l.Root, filepath.FromSlash(LegacyZone)) }
func (l Library) ReleaseDir(id string) string {
	return filepath.Join(l.ReleasesDir(), safeSegment(id))
}
func (l Library) StagingDir(taskID string) string {
	return filepath.Join(l.StagingRoot(), safeSegment(taskID))
}
func (l Library) IndexDocPath() string { return filepath.Join(l.Root, "INDEX.md") }

// safeSegment keeps a generated identifier usable as a single path element.
func safeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "_"
	}
	return out
}

// Ensure creates the directory skeleton. Only the zones that are needed are
// created; the library does not pre-generate empty topic directories.
func (l Library) Ensure() error {
	for _, dir := range []string{l.Root, l.ContentDir(), l.CatalogDir(), l.SourcesDir(), l.SystemDir(), l.StagingRoot(), l.ReleasesDir(), l.ReprDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("%w: create library dir %s: %v", ErrValidation, dir, err)
		}
	}
	return nil
}

// ContentPath converts an absolute staged document path into the
// library-relative content path, rejecting anything outside the zone.
func (l Library) ContentPath(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("%w: document path %q escapes the content zone", ErrValidation, rel)
	}
	if !strings.HasSuffix(clean, ".md") {
		return "", fmt.Errorf("%w: document path %q is not markdown", ErrValidation, rel)
	}
	return filepath.Join(l.ContentDir(), clean), nil
}

// ── Staging contract ────────────────────────────────────────────────────

// Position is a 0-based, half-open text coordinate inside a representation.
type Position struct {
	Line   int `yaml:"line" json:"line"`
	Column int `yaml:"column" json:"column"`
}

// Locator names a fragment of one frozen representation.
type Locator struct {
	Kind       string   `yaml:"kind" json:"kind"`
	LineBase   int      `yaml:"line_base" json:"line_base"`
	ColumnBase int      `yaml:"column_base" json:"column_base"`
	ColumnUnit string   `yaml:"column_unit" json:"column_unit"`
	Interval   string   `yaml:"interval" json:"interval"`
	Start      Position `yaml:"start" json:"start"`
	End        Position `yaml:"end" json:"end"`
	Quote      string   `yaml:"quote" json:"quote"`
}

// EvidenceRequest is the model's *request* for evidence. It names a source
// binding, a file and a locator; the collector computes identity, content and
// digest. No digest, approval or verification field is accepted here.
type EvidenceRequest struct {
	Key     string  `yaml:"key" json:"key"`
	Binding string  `yaml:"binding" json:"binding"`
	Path    string  `yaml:"path" json:"path"`
	Locator Locator `yaml:"locator" json:"locator"`
	Note    string  `yaml:"note" json:"note"`
}

// Coverage is the task's own account of what was and was not examined.
type Coverage struct {
	SourcesRead   []string `json:"sources_read"`
	SourcesMissed []string `json:"sources_missed"`
	Notes         string   `json:"notes"`
	Gaps          []string `json:"gaps"`
}

// TaskPlan is the staging plan.json the librarian agent must produce.
type TaskPlan struct {
	Summary   string   `json:"summary"`
	Documents []string `json:"documents"`
	Removals  []string `json:"removals"`
	Renames   []Rename `json:"renames"`
	Coverage  Coverage `json:"coverage"`
}

// Rename records a logical document move that keeps its stable ID.
type Rename struct {
	DocumentID string `json:"document_id"`
	FromPath   string `json:"from_path"`
	ToPath     string `json:"to_path"`
	Note       string `json:"note"`
}

// StagedEvidenceFile is the staging evidence.yaml document.
type StagedEvidenceFile struct {
	Evidence []EvidenceRequest `yaml:"evidence"`
}

// Staged is one fully-read staging directory.
type Staged struct {
	TaskID    string
	Dir       string
	Plan      TaskPlan
	Evidence  []EvidenceRequest
	Entities  []EntitySpec
	Documents []StagedDocument
}

// StagedDocument is one parsed staged markdown file.
type StagedDocument struct {
	RelPath string
	Doc     *Document
	Raw     []byte
}

// ReadStaging reads and parses everything a librarian turn produced.
// Missing optional files are tolerated; a malformed required file is not.
func (l Library) ReadStaging(taskID string) (*Staged, error) {
	dir := l.StagingDir(taskID)
	out := &Staged{TaskID: taskID, Dir: dir}
	planRaw, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	switch {
	case err == nil:
		if err := strictJSON(planRaw, &out.Plan); err != nil {
			return nil, fmt.Errorf("%w: plan.json: %v", ErrValidation, err)
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	evRaw, err := os.ReadFile(filepath.Join(dir, "evidence.yaml"))
	switch {
	case err == nil:
		// The documented shape is a top-level `evidence:` list; a bare list is
		// accepted too so a minimal file still parses.
		var file StagedEvidenceFile
		dec := yaml.NewDecoder(strings.NewReader(string(evRaw)))
		dec.KnownFields(true)
		if err := dec.Decode(&file); err != nil {
			var bare []EvidenceRequest
			bareDec := yaml.NewDecoder(strings.NewReader(string(evRaw)))
			bareDec.KnownFields(true)
			if bareErr := bareDec.Decode(&bare); bareErr != nil {
				return nil, fmt.Errorf("%w: evidence.yaml: %v", ErrValidation, err)
			}
			file.Evidence = bare
		}
		out.Evidence = file.Evidence
	case !os.IsNotExist(err):
		return nil, err
	}
	seen := map[string]bool{}
	for _, req := range out.Evidence {
		if strings.TrimSpace(req.Key) == "" {
			return nil, fmt.Errorf("%w: evidence.yaml 含空 key", ErrValidation)
		}
		if seen[req.Key] {
			return nil, fmt.Errorf("%w: evidence.yaml 中 key %q 重复", ErrValidation, req.Key)
		}
		seen[req.Key] = true
	}
	entRaw, err := os.ReadFile(filepath.Join(dir, "entities.yaml"))
	switch {
	case err == nil:
		dec := yaml.NewDecoder(strings.NewReader(string(entRaw)))
		dec.KnownFields(true)
		var file struct {
			Entities []EntitySpec `yaml:"entities"`
		}
		if err := dec.Decode(&file); err != nil {
			return nil, fmt.Errorf("%w: entities.yaml: %v", ErrValidation, err)
		}
		out.Entities = file.Entities
	case !os.IsNotExist(err):
		return nil, err
	}
	contentRoot := filepath.Join(dir, ContentZone)
	if _, err := os.Stat(contentRoot); os.IsNotExist(err) {
		return out, nil
	}
	// Every malformed document is reported together so one repair turn can fix
	// all of them instead of rediscovering them one at a time.
	problems := &ValidationErrors{}
	err = filepath.WalkDir(contentRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "_sources" || strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(contentRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		doc, err := ParseDocument(rel, Canonicalize(raw))
		if err != nil {
			problems.Add(err)
			return nil
		}
		out.Documents = append(out.Documents, StagedDocument{RelPath: rel, Doc: doc, Raw: raw})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := problems.OrNil(); err != nil {
		return nil, err
	}
	sort.Slice(out.Documents, func(i, j int) bool { return out.Documents[i].RelPath < out.Documents[j].RelPath })
	return out, nil
}

func strictJSON(raw []byte, target any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}

// ResetStaging deletes and recreates a staging directory so a retry never
// inherits a partially written previous attempt.
func (l Library) ResetStaging(taskID string) (string, error) {
	dir := l.StagingDir(taskID)
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	for _, sub := range []string{dir, filepath.Join(dir, ContentZone)} {
		if err := os.MkdirAll(sub, 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// ── Frozen representations ──────────────────────────────────────────────

// StoreRepresentation writes frozen bytes under the content-addressed
// representation store and returns the relative stored path.
func (l Library) StoreRepresentation(digest string, content []byte) (string, error) {
	algo, hexPart, ok := strings.Cut(digest, ":")
	if !ok || hexPart == "" {
		return "", fmt.Errorf("%w: malformed digest %q", ErrValidation, digest)
	}
	sub := filepath.Join(l.ReprDir(), algo)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return "", err
	}
	name := hexPart + ".bin"
	target := filepath.Join(sub, name)
	if _, err := os.Stat(target); err == nil {
		return filepath.ToSlash(filepath.Join(algo, name)), nil
	}
	tmp, err := os.CreateTemp(sub, ".tmp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(algo, name)), nil
}

// ReadRepresentation returns previously frozen bytes.
func (l Library) ReadRepresentation(storedPath string) ([]byte, error) {
	return os.ReadFile(filepath.Join(l.ReprDir(), filepath.FromSlash(storedPath)))
}

// DigestBytes is the canonical content digest used across the library.
func DigestBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ShortID derives a stable short identity suffix from content.
func ShortID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:20]
}
