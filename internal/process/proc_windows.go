//go:build windows

package process

import (
	"os/exec"
	"strconv"
)

// signal0alive Windows 无 kill(0) 探测, 返回 false (用命令行探测代替)
func signal0alive(pid int) bool {
	return false
}

// killProcess Windows 树杀: taskkill /T /F
func killProcess(pid int, tree bool) error {
	args := []string{"/F", "/PID", strconv.Itoa(pid)}
	if tree {
		args = append([]string{"/T"}, args...)
	}
	return exec.Command("taskkill", args...).Run()
}

// applySysProcAttr Windows 不需 setpgid
func applySysProcAttr(cmd *exec.Cmd) {}