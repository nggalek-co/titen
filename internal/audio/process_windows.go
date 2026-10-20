package audio

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func configureHelperCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
