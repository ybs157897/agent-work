package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestParseCLIArgsAllowsFlagsAfterRecordContent(t *testing.T) {
	fs := flag.NewFlagSet("atw-knowledge", flag.ContinueOnError)
	intent := fs.String("publish-intent", "", "")
	title := fs.String("title", "", "")
	args, help, err := parseCLIArgs(fs, []string{"record", "用户确认的规则", "--publish-intent", "confirmed_requirement", "--title", "规则"})
	if err != nil || help || len(args) != 2 || args[0] != "record" || args[1] != "用户确认的规则" {
		t.Fatalf("record command/arguments were not preserved: args=%v help=%t err=%v", args, help, err)
	}
	if *intent != "confirmed_requirement" || *title != "规则" {
		t.Fatalf("flags after record content were not parsed: intent=%q title=%q", *intent, *title)
	}
}

func TestParseCLIArgsTreatsHelpAsLocalHelp(t *testing.T) {
	fs := flag.NewFlagSet("atw-knowledge", flag.ContinueOnError)
	fs.String("publish-intent", "", "")
	args, help, err := parseCLIArgs(fs, []string{"record", "--help"})
	if err != nil || !help || args != nil {
		t.Fatalf("record --help was treated as content: args=%v help=%t err=%v", args, help, err)
	}
}

func TestDefaultQueryTimeoutAllowsServerBudgetToFinish(t *testing.T) {
	serverBudget := time.Duration((domain.KnowledgeJobBudget{}).Normalize().MaxDurationSeconds) * time.Second
	if defaultQueryTimeout() <= serverBudget {
		t.Fatal("the CLI must not abandon a query before the server's default Job budget ends")
	}
}

func TestLoadAccessFileReadsCapabilityWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(path, []byte(`{"url":"http://127.0.0.1:8080/api/v1/knowledge-agent/runs/run_1","token":"secret-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	endpoint, token, err := loadAccessFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint == "" || token != "secret-token" {
		t.Fatalf("loaded capability = endpoint %q token %q", endpoint, token)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("access file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadAccessFileRejectsWeakPermissionsAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	weak := filepath.Join(dir, "weak.json")
	if err := os.WriteFile(weak, []byte(`{"url":"http://localhost","token":"secret"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAccessFile(weak); err == nil {
		t.Fatal("weakly-permissioned access file was accepted")
	}
	unknown := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"url":"http://localhost","token":"secret","extra":"leak"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAccessFile(unknown); err == nil {
		t.Fatal("access file with unknown field was accepted")
	}
	valid := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(valid, []byte(`{"url":"http://localhost","token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAccessFile(link); err == nil {
		t.Fatal("symlinked access file was accepted")
	}
}
