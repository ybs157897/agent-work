//go:build !unix

package kimi

import "os/exec"

type sigT int

const (
	sigInt  sigT = 0
	sigKill sigT = 1
)

func setProcGroup(cmd *exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, pgid int, sig sigT) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// processGroupID 非 unix 平台无进程组，退化为 pid。
func processGroupID(cmd *exec.Cmd) int {
	if cmd.Process != nil {
		return cmd.Process.Pid
	}
	return 0
}

func exitedBySignal(err error) bool { return false }
