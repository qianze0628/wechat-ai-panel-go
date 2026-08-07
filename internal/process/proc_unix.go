//go:build !windows

package process

import (
	"os"
	"os/exec"
	"syscall"
)

// signal0alive Unix 用 Signal(0) 探活
func signal0alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// killProcess Unix 树杀: kill 负 pid (需 setpgid 启动)
func killProcess(pid int, tree bool) error {
	if pid <= 0 {
		return nil
	}
	if tree {
		return syscall.Kill(-pid, syscall.SIGTERM)
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// applySysProcAttr Unix 启用 setpgid (进程组可树杀)
func applySysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}