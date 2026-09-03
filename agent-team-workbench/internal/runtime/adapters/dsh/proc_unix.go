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

// signalGroup 使用启动时缓存的 pgid。组长退出并被 Wait 收尸后现场
// Getpgid 会失败，但同组子进程仍需要被回收。组级信号失败时才退回组长。
func signalGroup(cmd *exec.Cmd, pgid int, sig syscall.Signal) {
	if pgid > 0 {
		if err := syscall.Kill(-pgid, sig); err == nil {
			return
		}
	}
	if cmd.Process != nil {
		_ = cmd.Process.Signal(sig)
	}
}

func processGroupID(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		return pgid
	}
	return cmd.Process.Pid
}
