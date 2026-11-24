//go:build !unix

package integration

import "os/exec"

func runInOwnProcessGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Kill() }
}

func killStragglers(*exec.Cmd) {}
