package knowledgeclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const maxRunBoundCapabilityBytes = 64 << 10

type runBoundCapability struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// NewRunBound creates a knowledge client from the capability file attached to
// one Run. The file is the only place where the bearer token is read; callers
// must provide the token digest carried in Run.Input separately.
func NewRunBound(accessPath, tokenDigest, runID string) (*Client, error) {
	if strings.TrimSpace(accessPath) == "" || strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("knowledge capability path and Run ID are required")
	}
	if strings.ContainsAny(runID, `/\`) {
		return nil, fmt.Errorf("invalid Run ID")
	}
	info, err := os.Lstat(accessPath)
	if err != nil {
		return nil, fmt.Errorf("stat knowledge capability: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("knowledge capability must be a private 0600 regular file")
	}
	f, err := os.Open(accessPath)
	if err != nil {
		return nil, fmt.Errorf("read knowledge capability: %w", err)
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm() != 0o600 || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("knowledge capability changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxRunBoundCapabilityBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read knowledge capability: %w", err)
	}
	if len(raw) >= maxRunBoundCapabilityBytes {
		return nil, fmt.Errorf("knowledge capability is too large")
	}
	var capability runBoundCapability
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&capability); err != nil {
		return nil, fmt.Errorf("invalid knowledge capability")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid knowledge capability")
	}
	if strings.TrimSpace(capability.URL) == "" || capability.Token == "" {
		return nil, fmt.Errorf("knowledge capability is incomplete")
	}
	if err := validateRunBoundURL(capability.URL, runID); err != nil {
		return nil, err
	}
	if err := validateTokenDigest(capability.Token, tokenDigest); err != nil {
		return nil, err
	}
	return New(capability.URL, capability.Token)
}

func validateRunBoundURL(rawURL, runID string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("knowledge capability URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	wantPath := "/api/v1/knowledge-agent/runs/" + url.PathEscape(runID)
	if u.EscapedPath() != wantPath {
		return fmt.Errorf("knowledge capability URL is not bound to the current Run")
	}
	return nil
}

func validateTokenDigest(token, tokenDigest string) error {
	digest := strings.TrimSpace(tokenDigest)
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("knowledge capability token digest is invalid")
	}
	want := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(decoded, want[:]) != 1 {
		return fmt.Errorf("knowledge capability token digest does not match")
	}
	return nil
}

type runBoundToolArguments struct {
	Action        string  `json:"action"`
	Question      *string `json:"question"`
	Content       *string `json:"content"`
	Title         *string `json:"title"`
	PublishIntent *string `json:"publish_intent"`
	ItemID        *string `json:"item_id"`
	Version       *int64  `json:"version"`
}

// Invoke exposes the small native tool surface used by a Run. Arguments are
// decoded strictly so a provider cannot smuggle workspace, Agent, URL, or
// credential selectors into the Run-bound client.
func (c *Client) Invoke(ctx context.Context, arguments json.RawMessage, key string) (json.RawMessage, error) {
	if c == nil {
		return nil, fmt.Errorf("knowledge client is required")
	}
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	args, err := decodeRunBoundToolArguments(arguments)
	if err != nil {
		return nil, err
	}
	switch args.Action {
	case "ask":
		if args.Question == nil || strings.TrimSpace(*args.Question) == "" {
			return nil, fmt.Errorf("ask requires question")
		}
		if args.Content != nil || args.Title != nil || args.PublishIntent != nil || args.ItemID != nil || args.Version != nil {
			return nil, fmt.Errorf("ask accepts only question")
		}
		input := map[string]any{"question": *args.Question}
		if key != "" {
			input["client_key"] = key
		}
		return c.Ask(ctx, input, key)
	case "read":
		if args.ItemID == nil || strings.TrimSpace(*args.ItemID) == "" || strings.ContainsAny(*args.ItemID, `/\`) {
			return nil, fmt.Errorf("read requires one knowledge item_id")
		}
		if args.Question != nil || args.Content != nil || args.Title != nil || args.PublishIntent != nil {
			return nil, fmt.Errorf("read accepts only item_id and optional positive version")
		}
		path := "/items/" + url.PathEscape(*args.ItemID)
		if args.Version != nil {
			if *args.Version <= 0 {
				return nil, fmt.Errorf("read version must be a positive integer")
			}
			path += "/versions/" + strconv.FormatInt(*args.Version, 10)
		}
		return c.Do(ctx, http.MethodGet, path, nil, "")
	case "record":
		if args.Content == nil || strings.TrimSpace(*args.Content) == "" {
			return nil, fmt.Errorf("record requires content")
		}
		if args.Question != nil || args.ItemID != nil || args.Version != nil {
			return nil, fmt.Errorf("record accepts content, title, and publish_intent")
		}
		if args.PublishIntent != nil && *args.PublishIntent != "" && *args.PublishIntent != "confirmed_requirement" {
			return nil, fmt.Errorf("record publish_intent is unsupported")
		}
		input := map[string]any{"content": *args.Content}
		if key != "" {
			input["client_key"] = key
		}
		if args.Title != nil {
			input["title"] = *args.Title
		}
		if args.PublishIntent != nil && *args.PublishIntent != "" {
			input["publish_intent"] = *args.PublishIntent
		}
		return c.Record(ctx, input, key)
	default:
		return nil, fmt.Errorf("unsupported knowledge action %q", args.Action)
	}
}

func decodeRunBoundToolArguments(raw json.RawMessage) (*runBoundToolArguments, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("knowledge tool arguments are required")
	}
	var args runBoundToolArguments
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("invalid knowledge tool arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("knowledge tool arguments must be one JSON object")
	}
	if strings.TrimSpace(args.Action) == "" {
		return nil, fmt.Errorf("knowledge action is required")
	}
	return &args, nil
}
