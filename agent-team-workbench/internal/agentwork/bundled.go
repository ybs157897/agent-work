package agentwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ResolveBundledBin 解析 Runtime CLI 路径：环境变量 > 仓库内置 runtimes/ > PATH 回退名。
func ResolveBundledBin(workbenchRoot, envKey, runtimeName, pathFallback string) string {
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return v
	}
	if bundled := BundledRuntime(workbenchRoot, runtimeName); bundled != "" {
		return bundled
	}
	return pathFallback
}

// BundledRuntime 返回 workbench 内置的 runtimes/<name>/<platform>/<bin>，不存在则返回空串。
func BundledRuntime(workbenchRoot, runtimeName string) string {
	platform := runtimePlatformDir()
	binName := runtimeName
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	candidates := []string{
		filepath.Join(workbenchRoot, "runtimes", runtimeName, platform, binName),
		// 兼容旧版扁平布局 runtimes/codex/codex
		filepath.Join(workbenchRoot, "runtimes", runtimeName, binName),
	}
	for _, p := range candidates {
		if ExecutableOK(p) {
			return p
		}
	}
	return ""
}

const gitLFSPointerHeader = "version https://git-lfs.github.com/spec/v1"

// ExecutableOK 判断可执行文件可用（PATH 名或文件路径）。Git LFS 未拉取时，
// 仓库里的“大文件”实际只是带可执行位的文本指针；这种文件必须视为不可用，
// 让解析逻辑继续回退到系统 PATH，而不是等到 Probe 才报 exec format error。
func ExecutableOK(bin string) bool {
	if strings.TrimSpace(bin) == "" {
		return false
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	header := make([]byte, len(gitLFSPointerHeader))
	n, _ := f.Read(header)
	return string(header[:n]) != gitLFSPointerHeader
}

func runtimePlatformDir() string {
	switch runtime.GOOS {
	case "darwin":
		switch runtime.GOARCH {
		case "arm64":
			return "darwin-arm64"
		case "amd64":
			return "darwin-amd64"
		}
	case "windows":
		switch runtime.GOARCH {
		case "amd64":
			return "windows-amd64"
		case "arm64":
			return "windows-arm64"
		}
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return "linux-amd64"
		case "arm64":
			return "linux-arm64"
		}
	}
	return runtime.GOOS + "-" + runtime.GOARCH
}
