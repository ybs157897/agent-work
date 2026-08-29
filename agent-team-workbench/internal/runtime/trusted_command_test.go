package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustedCommandRejectsEmpty(t *testing.T) {
	if _, err := TrustedCommand(""); err == nil {
		t.Fatal("空路径必须报错")
	}
	if _, err := TrustedCommand("   "); err == nil {
		t.Fatal("纯空白路径必须报错")
	}
}

func TestTrustedCommandRejectsMissing(t *testing.T) {
	if _, err := TrustedCommand("atw-definitely-not-exist-bin"); err == nil {
		t.Fatal("不存在的裸名必须报错")
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "not-here")
	if _, err := TrustedCommand(missing); err == nil {
		t.Fatal("不存在的带分隔符路径必须报错")
	}
}

func TestTrustedCommandResolvesBareNameViaPath(t *testing.T) {
	cmd, err := TrustedCommand("sh", "-c", "true")
	if err != nil {
		t.Fatalf("PATH 裸名解析失败: %v", err)
	}
	if cmd.Path == "" || !filepath.IsAbs(cmd.Path) {
		t.Fatalf("PATH 解析应得到绝对路径, got %q", cmd.Path)
	}
	if cmd.Args[0] != cmd.Path {
		t.Fatalf("Args[0] 应与解析后路径一致: %q vs %q", cmd.Args[0], cmd.Path)
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != "-c" || cmd.Args[2] != "true" {
		t.Fatalf("参数数组顺序/内容错误: %v", cmd.Args)
	}
}

func TestTrustedCommandAbsolutizesSeparatorPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-bin")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(mustGetwd(t), bin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.ContainsRune(rel, os.PathSeparator) {
		t.Skip("无法构造相对带分隔符路径")
	}
	cmd, err := TrustedCommand(rel)
	if err != nil {
		t.Fatalf("带分隔符相对路径解析失败: %v", err)
	}
	if !filepath.IsAbs(cmd.Path) {
		t.Fatalf("带分隔符路径应解析为绝对路径, got %q", cmd.Path)
	}
}

func TestTrustedCommandRejectsNonExecutable(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "no-exec")
	if err := os.WriteFile(bin, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TrustedCommand(bin); err == nil {
		t.Fatal("不可执行文件必须报错")
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
