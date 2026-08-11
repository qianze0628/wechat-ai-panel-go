package api

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAssetForPlatformMatchesRelease: 资产名必须与 release 实际命名一致
// (修复 2026-08-11: 之前返回 wechat-ai-panel-windows-amd64.zip → 下载 404)
func TestAssetForPlatformMatchesRelease(t *testing.T) {
	SetVersionTag("v0.5.2")
	asset := assetForPlatform()
	if asset == "" {
		t.Fatal("assetForPlatform 返回空")
	}
	// 必须含版本号和平台
	if !strings.Contains(asset, "0.5.2") {
		t.Errorf("资产名缺版本号: %s", asset)
	}
	if !strings.Contains(asset, runtime.GOOS) {
		t.Errorf("资产名缺平台: %s", asset)
	}
	// windows 必须是 .exe
	if runtime.GOOS == "windows" && !strings.HasSuffix(asset, ".exe") {
		t.Errorf("windows 资产应为 .exe: %s", asset)
	}
	t.Logf("assetForPlatform(%s/%s) = %s", runtime.GOOS, runtime.GOARCH, asset)
}

// TestWhich2PortableDirs: which2 必须含面板便携目录 (git/nodejs)
// (修复 2026-08-11: 便携 MinGit 装到 ~/.wechat-ai-panel/git 后 which2 检测不到)
func TestWhich2PortableDirs(t *testing.T) {
	home, _ := os.UserHomeDir()
	// 模拟便携 git 存在 (临时创建)
	gitDir := filepath.Join(home, ".wechat-ai-panel", "git", "cmd")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Skipf("无法创建测试目录: %v", err)
	}
	gitExe := filepath.Join(gitDir, "git.exe")
	tmpGit := gitExe + ".test"
	if err := os.WriteFile(tmpGit, []byte("test"), 0o644); err != nil {
		t.Skipf("无法写测试文件: %v", err)
	}
	// 暂存真实 git.exe (若有) 并放测试文件
	hadReal := false
	if _, err := os.Stat(gitExe); err == nil {
		hadReal = true
		_ = os.Rename(gitExe, gitExe+".real")
	}
	_ = os.Rename(tmpGit, gitExe)
	defer func() {
		_ = os.Remove(gitExe)
		if hadReal {
			_ = os.Rename(gitExe+".real", gitExe)
		}
	}()
	// which2 应能找到便携 git
	if p := which2("git"); p == "" {
		t.Error("which2 找不到便携 git (~/.wechat-ai-panel/git/cmd)")
	} else {
		t.Logf("which2 便携 git = %s", p)
	}
}
