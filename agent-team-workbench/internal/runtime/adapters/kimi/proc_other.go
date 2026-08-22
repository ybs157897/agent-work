//go:build !unix

package kimi

import "os/exec"

type sigT int

const (
	sigInt  sigT = 0
	sigKill sigT = 1
)

func setProcGroup(cmd *exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, sig sigT) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
