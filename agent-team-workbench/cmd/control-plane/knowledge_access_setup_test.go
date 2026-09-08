package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
)

func TestKnowledgeEndpointUsesConfiguredListenerWithoutGuessingPort(t *testing.T) {
	for address, want := range map[string]string{
		":53225": "http://127.0.0.1:53225", "127.0.0.1:61234": "http://127.0.0.1:61234",
		"[::]:41234": "http://[::1]:41234", "192.168.1.5:43210": "http://192.168.1.5:43210",
		":0": "", ":70000": "", "missing-port": "",
	} {
		if got := localKnowledgeEndpoint(address); got != want {
			t.Fatalf("endpoint(%q)=%q, want %q", address, got, want)
		}
	}
}

func TestKnowledgeClientDiscoversBuiltClientAndRejectsInvalidExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin", "atw-knowledge")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATW_KNOWLEDGE_CLIENT", "")
	if got := resolveKnowledgeClient(dir); got != path {
		t.Fatalf("client=%q, want repository-built client %q", got, path)
	}
	t.Setenv("ATW_KNOWLEDGE_CLIENT", "bin/missing")
	if got := resolveKnowledgeClient(dir); got != "" {
		t.Fatalf("invalid explicit client must not silently select another executable: %q", got)
	}
	t.Setenv("ATW_KNOWLEDGE_CLIENT", "bin/atw-knowledge")
	if got := resolveKnowledgeClient(dir); got != path {
		t.Fatalf("relative client must be sealed to workbench root: %q", got)
	}
}

func TestNativeKnowledgeEndpointDoesNotRequireShellClient(t *testing.T) {
	t.Setenv("ATW_KNOWLEDGE_ENDPOINT", "http://127.0.0.1:12345")
	t.Setenv("ATW_KNOWLEDGE_CLIENT", "missing-client")
	svc := &application.Service{}
	configureKnowledgeAccess(svc, ":0", t.TempDir())
	if svc.KnowledgeEndpoint != "http://127.0.0.1:12345" || svc.KnowledgeCLIPath != "" {
		t.Fatal("native knowledge transport must keep its explicit endpoint without a shell client")
	}
}
