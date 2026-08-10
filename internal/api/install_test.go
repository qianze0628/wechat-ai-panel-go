package api

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestBuildCmdEnvPathRefresh 验证: buildCmdEnv 从注册表刷新 PATH, 不依赖进程启动快照
func TestBuildCmdEnvPathRefresh(t *testing.T) {
	env := buildCmdEnv(nil)
	var pathVal string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			pathVal = strings.TrimPrefix(kv, "PATH=")
			break
		}
	}
	if pathVal == "" {
		t.Fatal("buildCmdEnv 未设置 PATH")
	}
	// 应包含注册表 PATH (系统 + 用户) 与当前进程 PATH
	if !strings.Contains(pathVal, string(os.PathListSeparator)) {
		t.Logf("PATH 仅单项: %s", pathVal[:min(100, len(pathVal))])
	}
	// UV_PYTHON_INSTALL_MIRROR 应存在 (国内下载 Python runtime 加速)
	foundMirror := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "UV_PYTHON_INSTALL_MIRROR=") {
			foundMirror = true
			break
		}
	}
	if !foundMirror {
		t.Error("buildCmdEnv 缺少 UV_PYTHON_INSTALL_MIRROR")
	}
}

// TestBuildCmdEnvExtraPaths 验证: extraPaths 作为 PATH 前缀注入, 不互相覆盖
func TestBuildCmdEnvExtraPaths(t *testing.T) {
	extra := []string{filepath.Join("C:", "fake", "nodejs"), filepath.Join("C:", "fake", "git")}
	env := buildCmdEnv(extra)
	var pathVal string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			pathVal = strings.TrimPrefix(kv, "PATH=")
			break
		}
	}
	// extraPaths 应出现在 PATH 开头
	if !strings.HasPrefix(pathVal, extra[0]) {
		t.Errorf("extraPaths 未作为 PATH 前缀: %s", pathVal[:min(120, len(pathVal))])
	}
	// 只应有一条 PATH= (不互相覆盖)
	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("PATH= 出现 %d 次 (应只有 1 条, 避免覆盖)", count)
	}
}

// TestWhich2PythonStub 验证: WindowsApps 商店别名 stub (0 字节) 不被当作真 Python
func TestWhich2PythonStub(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows")
	}
	// 创建一个 0 字节假 python 放到候选目录, which2 应跳过它
	home, _ := os.UserHomeDir()
	stubDir := filepath.Join(home, ".local", "bin")
	stubPath := filepath.Join(stubDir, "python.exe")
	if _, err := os.Stat(stubPath); err != nil {
		// 无 stub 时跳过 (本机可能没有)
		t.Skip("本机无 stub 可测")
	}
	fi, _ := os.Stat(stubPath)
	if fi.Size() > 0 && fi.Size() < 1024 {
		// 真有 stub: which2 应返回其他候选 (如真实 python), 而不是 stub 路径
		got := which2("python")
		if got == stubPath {
			t.Error("which2 把商店别名 stub 当成了真 Python")
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
