//go:build unix

package dsh

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestSignalGroupUsesCachedPGIDAfterLeaderReaped(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	bin := filepath.Join(dir, "dsh-supervisor-fixture")
	script := "#!/bin/sh\n" +
		"sh -c 'echo $$ > " + pidFile + "; sleep 30' &\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd, err := atwruntime.TrustedCommand(bin)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = dir
	cmd.Env = os.Environ()
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := processGroupID(cmd)
	if pgid <= 0 {
		t.Fatalf("failed to sample process group at start: %d", pgid)
	}

	deadline := time.Now().Add(5 * time.Second)
	childPID := 0
	for time.Now().Before(deadline) {
		if raw, readErr := os.ReadFile(pidFile); readErr == nil {
			if value, parseErr := strconv.Atoi(strings.TrimSpace(string(raw))); parseErr == nil {
				childPID = value
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("fixture child pid was not written")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reap fixture leader: %v", err)
	}

	signalGroup(cmd, pgid, sigKill)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("orphan child pid=%d survived cached-pgid SIGKILL", childPID)
}
