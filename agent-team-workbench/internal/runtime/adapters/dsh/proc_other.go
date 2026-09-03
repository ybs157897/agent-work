//go:build !unix

package dsh

import "os/exec"

// Windows：M2 目标平台为 macOS/Linux；此处以单进程信号退化实现，
// 生产 Windows 支持需要 job object / process-tree 终止（协议文档 §8.3）。
const (
	sigInt  = sigFallback
	sigTerm = sigFallback
	sigKill = sigFallback
)

type sigFallbackT int

const sigFallback sigFallbackT = 0

func setProcGroup(cmd *exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, pgid int, sig sigFallbackT) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func processGroupID(cmd *exec.Cmd) int {
	if cmd.Process != nil {
		return cmd.Process.Pid
	}
	return 0
}
