package application

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeRunFilePath(t *testing.T) {
	accept := map[string]string{
		"notes/plan.md":       "notes/plan.md",
		"notes/proposed/a.md": "notes/proposed/a.md",
		"  notes/plan.md  ":   "notes/plan.md",
		"./notes/plan.md":     "notes/plan.md",
	}
	for input, want := range accept {
		got, err := normalizeRunFilePath(input)
		if err != nil {
			t.Fatalf("合法路径被拒 %q: %v", input, err)
		}
		if got != want {
			t.Fatalf("路径未收敛 %q → %q（期望 %q）", input, got, want)
		}
	}
	reject := []string{
		"", "/etc/passwd", "../secret.md", "notes/../../secret.md", `C:\windows\x.md`,
		".git/config", ".git", "nul\x00path", strings.Repeat("a", RunFilePreviewMaxPath+1),
		// 段内 ".." 一律拒绝：与 changes/diff 同一口径，合法调用不需要回退段。
		"notes/./deep/../a.md", "notes/deep/..",
	}
	for _, input := range reject {
		if _, err := normalizeRunFilePath(input); !errors.Is(err, ErrRunFileInvalidPath) {
			t.Fatalf("非法路径 %q 未被拒: %v", input, err)
		}
	}
}

func TestReadRunFilePreviewRejectsBinaryOversizeAndDirectory(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(text, []byte("# 计划\n\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(binary, []byte{0x00, 0x01, 0x02, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken.txt")
	if err := os.WriteFile(broken, []byte{0xff, 0xfe, 0xfd}, 0o644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.md")
	if err := os.WriteFile(big, make([]byte, RunFilePreviewMaxBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readRunFilePreview(text)
	if err != nil {
		t.Fatalf("文本文件应可预览: %v", err)
	}
	if got.text != "# 计划\n\n正文\n" || got.size != int64(len(got.text)) {
		t.Fatalf("预览内容不符: %+v", got)
	}
	if _, err := readRunFilePreview(binary); !errors.Is(err, ErrRunFileUnsupported) {
		t.Fatalf("二进制文件必须被拒: %v", err)
	}
	if _, err := readRunFilePreview(broken); !errors.Is(err, ErrRunFileUnsupported) {
		t.Fatalf("非 UTF-8 文件必须被拒: %v", err)
	}
	if _, err := readRunFilePreview(big); !errors.Is(err, ErrRunFileTooLarge) {
		t.Fatalf("超限文件必须被拒: %v", err)
	}
	if _, err := readRunFilePreview(dir); !errors.Is(err, ErrRunFileUnsupported) {
		t.Fatalf("目录必须被拒: %v", err)
	}
	if _, err := readRunFilePreview(filepath.Join(dir, "missing.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺失文件必须报不存在: %v", err)
	}
}

func TestPreviewMIME(t *testing.T) {
	// Markdown 必须稳定识别（前端据此选渲染器），不依赖宿主 mime.types。
	if got := previewMIME("notes/plan.md"); got != "text/markdown" {
		t.Fatalf("previewMIME(md) = %q", got)
	}
	if got := previewMIME("notes/PLAN.MARKDOWN"); got != "text/markdown" {
		t.Fatalf("previewMIME(markdown) = %q", got)
	}
	if got := previewMIME("notes/unknown.foo"); got != "text/plain" {
		t.Fatalf("未知扩展名必须回落 text/plain，得到 %q", got)
	}
	if got := previewMIME("notes/notes.txt"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("txt 应落在 text/plain 族，得到 %q", got)
	}
}
