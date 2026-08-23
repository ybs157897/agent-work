//go:build unix

package codexapp

import (
	"os/exec"
	"syscall"
)

const (
	sigTerm = syscall.SIGTERM
	sigInt  = syscall.SIGINT
	sigKill = syscall.SIGKILL
)

func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup 向进程组发送信号；pgid 必须是启动时采样的缓存值——
// 组长死亡（僵尸）后 Getpgid 失败，若现场重查会导致组级信号永远打不出去。
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

// processGroupID 返回进程组 id（Setpgid 后通常等于 pid），供 OnSpawn 上报。
func processGroupID(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		return pgid
	}
	return cmd.Process.Pid
}
