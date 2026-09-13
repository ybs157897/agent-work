package application

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// Run 文件只读预览（RFC §4.3 的 Host 本地信任边界之内）：
// 客户端只能给「仓库相对路径」，服务端在 Run 执行上下文授权的仓库集合里解析，
// 绝对路径、`..` 逃逸、symlink 穿透与 .git 内部文件一律拒绝；返回体永远只有
// 相对路径与文件内容，不携带宿主绝对路径，也不提供目录列举。
const (
	// RunFilePreviewMaxBytes 是单个文件可预览的上限：超过即拒绝（不截断），
	// 避免把半份文档伪装成完整内容。
	RunFilePreviewMaxBytes = 1 << 20
	// RunFilePreviewMaxPath 限制请求路径长度。
	RunFilePreviewMaxPath = 512
)

var (
	ErrRunFileInvalidPath = errors.New("run file path is invalid")
	ErrRunFileNotFound    = errors.New("run file not found")
	ErrRunFileUnsupported = errors.New("run file cannot be previewed")
	ErrRunFileTooLarge    = errors.New("run file is too large to preview")
	ErrRunFileUnavailable = errors.New("run file preview is unavailable")
)

// RunFileContent 是只读预览的响应体：Path/Name 均为仓库相对口径。
type RunFileContent struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	MIME    string `json:"mime"`
	Size    int64  `json:"size"`
	Content string `json:"content"`
}

// SetRunFileRootsResolver 安装 Host 本地信任钩子：按 Run 的执行上下文快照返回
// 可只读预览的宿主目录集合（Run 自己的 checkout + 同一仓库的 worktree）。返回的
// 绝对路径只在本进程内使用，调用方无法提供路径。
func (s *Service) SetRunFileRootsResolver(v func(ctx context.Context, snapshot *domain.ExecutionContextSnapshot) ([]string, error)) {
	s.runFileRoots = v
}

func (s *Service) ReadRunFile(ctx context.Context, runID, relPath string) (RunFileContent, error) {
	if _, err := s.store.Runs().Get(ctx, runID); err != nil {
		return RunFileContent{}, err
	}
	rel, err := normalizeRunFilePath(relPath)
	if err != nil {
		return RunFileContent{}, err
	}
	snapshot, err := s.store.ContextSnapshots().GetByRun(ctx, runID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return RunFileContent{}, fmt.Errorf("%w: 该 Run 没有执行上下文快照", ErrRunFileUnavailable)
		}
		return RunFileContent{}, err
	}
	if snapshot.ExecutionHostID != domain.LocalHostID {
		return RunFileContent{}, fmt.Errorf("%w: 远程执行宿主暂无站内预览通道", ErrRunFileUnavailable)
	}
	if s.runFileRoots == nil {
		return RunFileContent{}, fmt.Errorf("%w: 本进程未挂载 Host 受信 registry", ErrRunFileUnavailable)
	}
	roots, err := s.runFileRoots(ctx, snapshot)
	if err != nil {
		return RunFileContent{}, fmt.Errorf("%w: %v", ErrRunFileUnavailable, err)
	}
	for _, root := range roots {
		abs, joinErr := secureJoin(root, rel)
		if joinErr != nil {
			continue
		}
		content, readErr := readRunFilePreview(abs)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return RunFileContent{}, readErr
		}
		return RunFileContent{
			Path:    rel,
			Name:    path.Base(rel),
			MIME:    previewMIME(rel),
			Size:    content.size,
			Content: content.text,
		}, nil
	}
	return RunFileContent{}, fmt.Errorf("%w: %s", ErrRunFileNotFound, rel)
}

// normalizeRunFilePath 收敛并校验请求路径：只接受干净的仓库相对路径。
func normalizeRunFilePath(raw string) (string, error) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" || len(candidate) > RunFilePreviewMaxPath {
		return "", fmt.Errorf("%w: 路径为空或过长", ErrRunFileInvalidPath)
	}
	if !validChangePath(candidate) {
		return "", fmt.Errorf("%w: 必须是干净的仓库相对路径", ErrRunFileInvalidPath)
	}
	cleaned := path.Clean(candidate)
	if cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: 路径必须指向仓库内文件", ErrRunFileInvalidPath)
	}
	if strings.SplitN(cleaned, "/", 2)[0] == ".git" {
		return "", fmt.Errorf("%w: 不接受版本库内部文件", ErrRunFileInvalidPath)
	}
	return cleaned, nil
}

type runFilePreview struct {
	text string
	size int64
}

// readRunFilePreview 读取一个普通文本文件：目录、二进制与非 UTF-8 内容、
// 超限文件都响亮拒绝，绝不返回半份内容。
func readRunFilePreview(abs string) (runFilePreview, error) {
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return runFilePreview{}, os.ErrNotExist
		}
		return runFilePreview{}, fmt.Errorf("%w: 无法读取该文件", ErrRunFileUnavailable)
	}
	if !info.Mode().IsRegular() {
		return runFilePreview{}, fmt.Errorf("%w: 不是普通文件", ErrRunFileUnsupported)
	}
	if info.Size() > RunFilePreviewMaxBytes {
		return runFilePreview{}, fmt.Errorf("%w: %d 字节超过 %d 字节上限", ErrRunFileTooLarge, info.Size(), RunFilePreviewMaxBytes)
	}
	handle, err := os.Open(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return runFilePreview{}, os.ErrNotExist
		}
		return runFilePreview{}, fmt.Errorf("%w: 无法读取该文件", ErrRunFileUnavailable)
	}
	defer func() { _ = handle.Close() }()
	raw, err := io.ReadAll(io.LimitReader(handle, RunFilePreviewMaxBytes+1))
	if err != nil {
		return runFilePreview{}, fmt.Errorf("%w: 读取过程中断", ErrRunFileUnavailable)
	}
	if len(raw) > RunFilePreviewMaxBytes {
		return runFilePreview{}, fmt.Errorf("%w: 超过 %d 字节上限", ErrRunFileTooLarge, RunFilePreviewMaxBytes)
	}
	if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
		return runFilePreview{}, fmt.Errorf("%w: 二进制或非 UTF-8 内容", ErrRunFileUnsupported)
	}
	return runFilePreview{text: string(raw), size: info.Size()}, nil
}

// previewMIME 只做展示用途：先认 Markdown（前端据此选渲染器），其余按扩展名
// 推断，未知一律 text/plain。不依赖宿主 mime.types，结果跨机器稳定。
func previewMIME(rel string) string {
	ext := strings.ToLower(path.Ext(rel))
	if ext == ".md" || ext == ".markdown" {
		return "text/markdown"
	}
	if detected := mime.TypeByExtension(ext); detected != "" {
		return detected
	}
	return "text/plain"
}
