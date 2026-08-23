//go:build unix

package kimi

import (
	"errors"
	"os/exec"
	"syscall"
)

const (
	sigInt  = syscall.SIGINT
	sigKill = syscall.SIGKILL
)

func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup 向进程组发送信号；pgid 必须是启动时采样的缓存值——
// 组长死亡（僵尸/已被收尸）后 Getpgid 失败，若现场重查会导致组级信号
// 永远打不出去（孤儿成员持管道时 Execute 随之挂起）。
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

// processGroupID 返回进程组 id（Setpgid 后通常等于 pid），供 OnSpawn 上报；
// 必须在组长存活时调用并缓存结果。
func processGroupID(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		return pgid
	}
	return cmd.Process.Pid
}

// exitedBySignal 区分信号终止与真实退出码：回收 SIGINT 击中已定终态的 CLI
// 不构成失败（与旧实现忽略回收后退出码一致）。
func exitedBySignal(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled()
}
