//go:build !unix

package codexapp

import "os/exec"

type sigT int

const (
	sigTerm sigT = 0
	sigKill sigT = 1
)

func setProcGroup(cmd *exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, sig sigT) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
