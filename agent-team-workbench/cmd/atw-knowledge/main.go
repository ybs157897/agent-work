// atw-knowledge is a JSON CLI for scoped knowledge access from any shell-capable Harness.
package main

import (
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("atw-knowledge", flag.ContinueOnError)
	endpoint := fs.String("url", os.Getenv("ATW_KNOWLEDGE_URL"), "Run-bound knowledge endpoint")
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
	client, err := knowledgeclient.New(*endpoint, os.Getenv("ATW_KNOWLEDGE_TOKEN"))
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
