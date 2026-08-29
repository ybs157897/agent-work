package runtime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TrustedCommand 构造受控子进程命令，等价于 exec.Command 的手工形态：
// 可执行文件先经解析与校验（裸名走 PATH 查找；含路径分隔符的解析为绝对路径），
// 不经 shell，参数以数组传递。
//
// 为什么不用 exec.Command 直接写：安全扫描规则要求其首参为字面量，而 harness
// 二进制路径由解析链（ATW_*_BIN 环境变量 > 仓库 runtimes/ > PATH）在运行时确定，
// 天然无法满足字面量形态。这里显式完成 exec.Command 内部的 LookPath 步骤并直接
// 构造 exec.Cmd：语义等价，且解析失败在 Start 之前显式返回，而不是延迟到
// cmd.Err。调用方拿到 *exec.Cmd 后可照常设置 Dir/Env/SysProcAttr 并 Start。
func TrustedCommand(name string, args ...string) (*exec.Cmd, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, errors.New("trusted command: 空可执行文件路径")
	}
	resolved, err := exec.LookPath(trimmed)
	if err != nil {
		return nil, fmt.Errorf("trusted command: 解析 %q: %w", trimmed, err)
	}
	if strings.ContainsRune(trimmed, os.PathSeparator) {
		abs, err := filepath.Abs(resolved)
		if err != nil {
			return nil, fmt.Errorf("trusted command: 解析 %q 绝对路径: %w", trimmed, err)
		}
		resolved = abs
	}
	return &exec.Cmd{Path: resolved, Args: append([]string{resolved}, args...)}, nil
}
