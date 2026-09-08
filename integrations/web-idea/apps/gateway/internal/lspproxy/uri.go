package lspproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ClientScheme is the browser-facing document URI scheme.
// Example: webidea://ws/{workspaceId}/src/Main.java
const ClientScheme = "webidea"

// ToClientURI maps an absolute file path under root to a client URI.
func ToClientURI(workspaceID, root, absPath string) (string, error) {
	if err := pathInsideRoot(root, absPath); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return fmt.Sprintf("%s://ws/%s", ClientScheme, url.PathEscape(workspaceID)), nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		escaped = append(escaped, url.PathEscape(part))
	}
	return fmt.Sprintf("%s://ws/%s/%s", ClientScheme, url.PathEscape(workspaceID), strings.Join(escaped, "/")), nil
}

// ToFileURI maps a client URI to file:///abs for jdtls.
func ToFileURI(workspaceID, root, clientURI string) (string, error) {
	u, err := url.Parse(clientURI)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(u.Scheme, "file") {
		abs, err := fileURIToPath(clientURI)
		if err != nil {
			return "", err
		}
		if err := pathInsideRoot(root, abs); err != nil {
			return "", err
		}
		return pathToFileURI(abs), nil
	}
	if u.Scheme != ClientScheme {
		return "", fmt.Errorf("unsupported uri scheme %q", u.Scheme)
	}
	if u.Host != "ws" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("invalid client uri")
	}
	// Host is "ws"; path is /{id}/rel...
	decodedPath, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return "", fmt.Errorf("invalid client uri")
	}
	if !strings.HasPrefix(decodedPath, "/") {
		return "", fmt.Errorf("invalid client uri")
	}
	parts := strings.Split(strings.TrimPrefix(decodedPath, "/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		return "", fmt.Errorf("invalid client uri")
	}
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	id := parts[0]
	if id != workspaceID {
		return "", fmt.Errorf("workspace id mismatch")
	}
	var relParts []string
	for _, part := range parts[1:] {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, 0) || strings.ContainsRune(part, '\\') {
			return "", fmt.Errorf("invalid client uri")
		}
		relParts = append(relParts, part)
	}
	abs := root
	if len(relParts) > 0 {
		abs = filepath.Join(append([]string{root}, relParts...)...)
	}
	if err := pathInsideRoot(root, abs); err != nil {
		return "", err
	}
	return pathToFileURI(abs), nil
}

func pathToFileURI(abs string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

func fileURIToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(u.Scheme, "file") {
		return "", fmt.Errorf("not a file uri")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Host != "" && u.Host != "localhost") {
		return "", fmt.Errorf("invalid file uri")
	}
	p, err := url.PathUnescape(u.EscapedPath())
	if err != nil || p == "" || strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("invalid file uri")
	}
	for _, segment := range strings.Split(filepath.ToSlash(p), "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid file uri")
		}
	}
	// Windows file:///C:/... — keep as-is for filepath
	return filepath.FromSlash(p), nil
}

func pathInsideRoot(root, candidate string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	rootAbs = filepath.Clean(rootAbs)
	candidateAbs = filepath.Clean(candidateAbs)
	if !isWithin(rootAbs, candidateAbs) {
		return fmt.Errorf("path outside root")
	}

	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		// URI conversion is also used for initialize messages before a test
		// fixture or a newly-created workspace root exists. The lexical jail
		// check above still prevents traversal in that case.
		if isNotExist(err) {
			return nil
		}
		return err
	}
	// Resolve the candidate, or its nearest existing parent, so a symlink
	// component cannot redirect an otherwise lexical in-root path outside.
	probe := candidateAbs
	for {
		resolved, resolveErr := filepath.EvalSymlinks(probe)
		if resolveErr == nil {
			if !isWithin(resolvedRoot, resolved) {
				return fmt.Errorf("path outside root")
			}
			return nil
		}
		if !isNotExist(resolveErr) {
			return resolveErr
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return nil
		}
		probe = parent
	}
}

func isWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist) || os.IsNotExist(err)
}

// RewriteJSON walks a JSON value and rewrites URI strings in both directions.
// direction "toServer": client → file://
// direction "toClient": file:// → client (jdt:// left untouched)
func RewriteJSON(raw []byte, workspaceID, root, direction string) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var err error
	v, err = rewriteValue(v, workspaceID, root, direction)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func rewriteValue(v any, workspaceID, root, direction string) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			// Common LSP keys that hold URIs
			if isURIKey(k) {
				if s, ok := child.(string); ok {
					ns, err := rewriteURIString(s, workspaceID, root, direction)
					if err != nil {
						// leave unknown schemes (jdt://) as-is for toClient
						if direction == "toClient" {
							continue
						}
						return nil, err
					}
					t[k] = ns
					continue
				}
			}
			nv, err := rewriteValue(child, workspaceID, root, direction)
			if err != nil {
				return nil, err
			}
			t[k] = nv
		}
		return t, nil
	case []any:
		for i, child := range t {
			nv, err := rewriteValue(child, workspaceID, root, direction)
			if err != nil {
				return nil, err
			}
			t[i] = nv
		}
		return t, nil
	default:
		return v, nil
	}
}

func isURIKey(k string) bool {
	switch k {
	case "uri", "rootUri", "targetUri", "document", "newUri", "oldUri":
		return true
	default:
		return false
	}
}

func rewriteURIString(s, workspaceID, root, direction string) (string, error) {
	if s == "" {
		return s, nil
	}
	switch direction {
	case "toServer":
		if strings.HasPrefix(s, ClientScheme+":") {
			return ToFileURI(workspaceID, root, s)
		}
		if u, err := url.Parse(s); err == nil && strings.EqualFold(u.Scheme, "file") {
			return "", fmt.Errorf("file uri must use %s scheme", ClientScheme)
		}
		return s, nil
	case "toClient":
		if u, err := url.Parse(s); err == nil && strings.EqualFold(u.Scheme, "file") {
			abs, err := fileURIToPath(s)
			if err != nil {
				return "", nil
			}
			if err := pathInsideRoot(root, abs); err != nil {
				// Do not expose a host path to the browser when jdtls emits a
				// location outside this workspace.
				return "", nil
			}
			clientURI, err := ToClientURI(workspaceID, root, abs)
			if err != nil {
				return "", nil
			}
			return clientURI, nil
		}
		return s, nil
	default:
		return s, fmt.Errorf("bad direction")
	}
}
