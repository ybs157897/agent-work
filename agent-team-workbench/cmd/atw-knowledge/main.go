// atw-knowledge is a JSON CLI for scoped knowledge access from any shell-capable Harness.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/knowledgeclient"
)

type accessFilePayload struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func defaultQueryTimeout() time.Duration {
	// The caller must wait long enough to receive the Job's own budget result.
	budget := (domain.KnowledgeJobBudget{}).Normalize()
	return time.Duration(budget.MaxDurationSeconds)*time.Second + time.Minute
}

func run() error {
	fs := flag.NewFlagSet("atw-knowledge", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: atw-knowledge [flags] ask QUESTION | record CONTENT | read ID [VERSION] | submit < changes.json")
		fs.PrintDefaults()
	}
	endpoint := fs.String("url", os.Getenv("ATW_KNOWLEDGE_URL"), "Run-bound knowledge endpoint")
	accessFile := fs.String("access-file", "", "Run-bound capability file")
	timeout := fs.Duration("timeout", defaultQueryTimeout(), "maximum query duration")
	key := fs.String("key", "", "stable idempotency key for retry")
	title := fs.String("title", "", "optional title for a raw Agent record")
	publishIntent := fs.String("publish-intent", "", "optional raw record publish intent")
	args, help, err := parseCLIArgs(fs, os.Args[1:])
	if err != nil {
		return err
	}
	if help {
		fs.Usage()
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: atw-knowledge [flags] ask QUESTION | record CONTENT | read ID [VERSION] | submit < changes.json")
	}
	if *timeout <= 0 || *timeout > 30*time.Minute {
		return fmt.Errorf("timeout must be between 0 and 30 minutes")
	}
	accessURL, accessToken := *endpoint, os.Getenv("ATW_KNOWLEDGE_TOKEN")
	if *accessFile != "" {
		accessURL, accessToken, err = loadAccessFile(*accessFile)
		if err != nil {
			return err
		}
	}
	client, err := knowledgeclient.New(accessURL, accessToken)
	if err != nil {
		return err
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, *timeout)
	defer cancel()
	if *key == "" {
		var b [16]byte
		if _, err = rand.Read(b[:]); err != nil {
			return err
		}
		*key = hex.EncodeToString(b[:])
	}
	var raw json.RawMessage
	switch args[0] {
	case "ask":
		if len(args) != 2 {
			return fmt.Errorf("ask requires one quoted question")
		}
		raw, err = client.Ask(ctx, map[string]any{"question": args[1], "client_key": *key}, *key)
	case "record", "ingest":
		if len(args) != 2 {
			return fmt.Errorf("record requires one quoted natural-language content")
		}
		input := map[string]any{"content": args[1], "client_key": *key}
		if *title != "" {
			input["title"] = *title
		}
		if *publishIntent != "" {
			input["publish_intent"] = *publishIntent
		}
		raw, err = client.Record(ctx, input, *key)
	case "read":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("read requires knowledge ID and optional version")
		}
		path := "/items/" + url.PathEscape(args[1])
		if len(args) == 3 {
			path += "/versions/" + url.PathEscape(args[2])
		}
		raw, err = client.Do(ctx, http.MethodGet, path, nil, "")
	case "submit":
		b, readErr := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
		if readErr != nil {
			return readErr
		}
		if len(b) > 1<<20 {
			return fmt.Errorf("submission exceeds 1 MiB")
		}
		var input map[string]any
		if err = json.Unmarshal(b, &input); err != nil {
			return fmt.Errorf("invalid submission JSON: %w", err)
		}
		if input == nil {
			return fmt.Errorf("submission must be a JSON object")
		}
		input["client_key"] = *key
		raw, err = client.Do(ctx, http.MethodPost, "/submissions", input, *key)
	default:
		return fmt.Errorf("unknown knowledge command %q", args[0])
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(raw))
	return err
}

// parseCLIArgs accepts the command before or after any global flag. The Go
// flag package stops at the first positional argument, while Agents naturally
// emit commands such as `record 'text' --publish-intent ...`; normalize the
// known flags before delegating value validation to flag.FlagSet.
func parseCLIArgs(fs *flag.FlagSet, raw []string) ([]string, bool, error) {
	normalized, help, err := normalizeCLIArgs(raw)
	if err != nil {
		return nil, false, err
	}
	if help {
		return nil, true, nil
	}
	if err := fs.Parse(normalized); err != nil {
		if err == flag.ErrHelp {
			return nil, true, nil
		}
		return nil, false, err
	}
	return fs.Args(), false, nil
}

func normalizeCLIArgs(raw []string) ([]string, bool, error) {
	flags := make([]string, 0, len(raw))
	positionals := make([]string, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		arg := raw[i]
		if arg == "--" {
			positionals = append(positionals, raw[i+1:]...)
			break
		}
		if arg == "-h" || arg == "-help" || arg == "--help" {
			return nil, true, nil
		}
		if known, takesValue := knowledgeCLIFlag(arg); known {
			flags = append(flags, arg)
			if !takesValue {
				continue
			}
			if i+1 >= len(raw) {
				return nil, false, fmt.Errorf("flag %s requires a value", arg)
			}
			i++
			flags = append(flags, raw[i])
			continue
		}
		if len(arg) > 0 && arg[0] == '-' {
			// Preserve unknown flags so FlagSet returns its canonical error.
			// Their following token remains positional unless it is itself a
			// known flag, matching the standard parser's fail-fast behavior.
			flags = append(flags, arg)
			continue
		}
		positionals = append(positionals, arg)
	}
	return append(flags, positionals...), false, nil
}

func knowledgeCLIFlag(arg string) (known, takesValue bool) {
	name := arg
	for len(name) > 0 && name[0] == '-' {
		name = name[1:]
	}
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		name = name[:eq]
	}
	switch name {
	case "url", "access-file", "timeout", "key", "title", "publish-intent":
		return true, true
	default:
		return false, false
	}
}

func loadAccessFile(path string) (string, string, error) {
	if path == "" {
		return "", "", fmt.Errorf("access file path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", fmt.Errorf("stat access file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", "", fmt.Errorf("access file must be a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("read access file: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return "", "", fmt.Errorf("read access file: %w", err)
	}
	if len(raw) == 64<<10 {
		return "", "", fmt.Errorf("access file is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var payload accessFilePayload
	if err := dec.Decode(&payload); err != nil {
		return "", "", fmt.Errorf("invalid access file")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return "", "", fmt.Errorf("invalid access file")
	}
	if payload.URL == "" || payload.Token == "" {
		return "", "", fmt.Errorf("access file is incomplete")
	}
	return payload.URL, payload.Token, nil
}
