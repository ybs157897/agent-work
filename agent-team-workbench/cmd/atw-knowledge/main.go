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
	"syscall"
	"time"

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

func run() error {
	fs := flag.NewFlagSet("atw-knowledge", flag.ContinueOnError)
	endpoint := fs.String("url", os.Getenv("ATW_KNOWLEDGE_URL"), "Run-bound knowledge endpoint")
	accessFile := fs.String("access-file", "", "Run-bound capability file")
	timeout := fs.Duration("timeout", 3*time.Minute, "maximum query duration")
	key := fs.String("key", "", "stable idempotency key for retry")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	args := fs.Args()
	if len(args) == 0 {
		return fmt.Errorf("usage: atw-knowledge [flags] ask QUESTION | read ID [VERSION] | submit < changes.json")
	}
	if *timeout <= 0 || *timeout > 30*time.Minute {
		return fmt.Errorf("timeout must be between 0 and 30 minutes")
	}
	accessURL, accessToken := *endpoint, os.Getenv("ATW_KNOWLEDGE_TOKEN")
	var err error
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
