//go:build !unix

package codexapp

import "os/exec"

type sigT int

const (
	sigTerm sigT = 0
	sigInt  sigT = 1
	sigKill sigT = 2
)

func setProcGroup(cmd *exec.Cmd) {}

// signalGroup 非 unix 平台无进程组，退化为直接终止进程。
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
