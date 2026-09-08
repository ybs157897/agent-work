package jdtls

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStopWaitsForProcessAndClosesSession(t *testing.T) {
	dir := t.TempDir()
	launch := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(launch, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Config{LaunchScript: launch, DataRoot: filepath.Join(dir, "data")})
	sess, err := m.Ensure("workspace", dir)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Done() == nil {
		t.Fatal("session has no lifecycle signal")
	}
	m.Stop("workspace")
	select {
	case <-sess.Done():
	default:
		t.Fatal("Stop returned before process exit")
	}
	if sess.Cmd.ProcessState == nil {
		t.Fatal("jdtls process state was not collected after Stop")
	}
	if _, err := m.Session("workspace"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("session retained after Stop: %v", err)
	}
}

func TestStopKeepsIndexUntilWorkspaceCleanup(t *testing.T) {
	dir := t.TempDir()
	dataRoot := filepath.Join(dir, "data")
	launch := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(launch, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Config{LaunchScript: launch, DataRoot: dataRoot})
	if _, err := m.Ensure("workspace", dir); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dataRoot, "workspace")
	if err := os.WriteFile(filepath.Join(dataDir, "marker"), []byte("index"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.Stop("workspace")
	if _, err := os.Stat(filepath.Join(dataDir, "marker")); err != nil {
		t.Fatalf("transient Stop removed index: %v", err)
	}
	m.StopAndCleanup("workspace")
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace data retained after cleanup: %v", err)
	}
}

func TestStopAllRemovesStaleGeneratedWorkspaceData(t *testing.T) {
	dir := t.TempDir()
	dataRoot := filepath.Join(dir, "data")
	id := "0123456789abcdef0123456789abcdef"
	dataDir := filepath.Join(dataRoot, id)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "marker"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Config{DataRoot: dataRoot})
	m.StopAll()
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale workspace data retained: %v", err)
	}
	if _, err := os.Stat(dataRoot); err != nil {
		t.Fatalf("StopAll removed data root: %v", err)
	}
}

func TestReadOnlyEnsureMarksLauncherEnvironment(t *testing.T) {
	dir := t.TempDir()
	launch := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(launch, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Config{LaunchScript: launch, DataRoot: filepath.Join(dir, "data")})
	sess, err := m.Ensure("workspace", dir, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range sess.Cmd.Env {
		if entry == "WEBIDEA_READ_ONLY=true" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("read-only launcher environment missing: %v", sess.Cmd.Env)
	}
	m.StopAndCleanup("workspace")
}
