//go:build unix

package dsh

import (
	"os/exec"
	"syscall"
)

const (
	sigInt  = syscall.SIGINT
	sigTerm = syscall.SIGTERM
	sigKill = syscall.SIGKILL
)

// setProcGroup 创建独立进程组：取消时可终止整棵树（协议文档 §8.3）。
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup 向整个进程组发信号；失败时退回单进程。
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, sig)
		return
	}
	_ = cmd.Process.Signal(sig)
}
