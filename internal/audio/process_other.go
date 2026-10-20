//go:build !windows

package audio

import "os/exec"

func configureHelperCommand(cmd *exec.Cmd) {}
