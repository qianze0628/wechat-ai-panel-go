// Package process 服务进程管理: 端口检测 / PID / 树杀 / 健康检查 / 派生进程
package process

import (
	"bufio"
	"errors"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// PortListening 检测本机端口是否监听 (跨平台)
func PortListening(port int) bool {
	for _, host := range []string{"127.0.0.1", "0.0.0.0"} {
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		conn, err := net.DialTimeout("tcp", addr, 600*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
	}
	return false
}

// GetPidOnPort 返回占用端口的 PID (Windows netstat / Linux ss/lsof)
func GetPidOnPort(port int) int {
	target := ":" + strconv.Itoa(port)
	if runtime.GOOS == "windows" {
		out, err := exec.Command("netstat", "-ano").Output()
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if !strings.Contains(line, "LISTENING") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 5 && strings.Contains(fields[1], target) {
				if pid, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
					return pid
				}
			}
		}
		return 0
	}
	if pid := ssPortPid(target); pid != 0 {
		return pid
	}
	return lsofPortPid(port)
}

func ssPortPid(target string) int {
	out, err := exec.Command("ss", "-ltnp").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, target) {
			continue
		}
		idx := strings.Index(line, "pid=")
		if idx >= 0 {
			pidStr := strings.Split(line[idx+4:], ",")[0]
			if pid, err := strconv.Atoi(pidStr); err == nil {
				return pid
			}
		}
	}
	return 0
}

func lsofPortPid(port int) int {
	out, err := exec.Command("lsof", "-i", ":"+strconv.Itoa(port), "-t").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if pid, err := strconv.Atoi(line); err == nil {
			return pid
		}
	}
	return 0
}

// GetPidCmdline 返回进程命令行 (Windows WMI, 其他 ps)
func GetPidCmdline(pid int) string {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", "-NoProfile", "-Command",
			"(Get-CimInstance Win32_Process -Filter 'ProcessId="+strconv.Itoa(pid)+"').CommandLine")
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// PidExists 判断进程是否存在 (通过命令行探测)
func PidExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	if signal0alive(pid) {
		return true
	}
	return GetPidCmdline(pid) != ""
}

// KillPid 结束进程; tree=true 时树杀 (由平台文件实现)
func KillPid(pid int, tree bool) error {
	return killProcess(pid, tree)
}

// SpawnOptions 派生进程选项
type SpawnOptions struct {
	Args   []string
	Dir    string
	Env    []string
	Stdout *os.File
	Stderr *os.File
}

// Spawn 启动后台进程 (输出到日志文件), 返回 *exec.Cmd
func Spawn(opts SpawnOptions) (*exec.Cmd, error) {
	if len(opts.Args) == 0 {
		return nil, errors.New("args 不能为空")
	}
	cmd := exec.Command(opts.Args[0], opts.Args[1:]...)
	cmd.Dir = opts.Dir
	if opts.Env != nil {
		cmd.Env = opts.Env
	}
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	applySysProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// BaseEnv 基础环境变量
func BaseEnv() []string {
	return append(os.Environ(), "PYTHONIOENCODING=utf-8")
}

// ReadTail 读取文件末尾 (限 maxBytes)
func ReadTail(path string, maxBytes int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return ""
	}
	size := fi.Size()
	start := size - minInt64(size, maxBytes)
	buf := make([]byte, size-start)
	_, _ = f.ReadAt(buf, start)
	return string(buf)
}

// LogReadOffset 从偏移读取 (供 SSE 增量); 返回 (新增内容, 新偏移)
func LogReadOffset(path string, from int64) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", from, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", from, err
	}
	if fi.Size() < from {
		// 文件轮转/截断: 重置偏移
		return "", 0, nil
	}
	if fi.Size() == from {
		return "", from, nil
	}
	buf := make([]byte, fi.Size()-from)
	if _, err := f.ReadAt(buf, from); err != nil && err.Error() != "EOF" {
		return string(buf), fi.Size(), nil
	}
	return string(buf), fi.Size(), nil
}

// bufio 备用引用 (Real)
var _ = bufio.ErrInvalidUnreadByte

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}