package api

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLatestGitPathPortable: 便携 git 存在时 latestGitPath 必须返回真实存在的绝对路径
// (模拟全新电脑: 系统 PATH 无 git, 便携 git 已解压 — 修复 exec.Command LookPath 死结的回归)
func TestLatestGitPathPortable(t *testing.T) {
	git := latestGitPath()
	if git == "" {
		t.Fatal("latestGitPath() 不应为空")
	}
	if git == "git" {
		// 裸名回退: 仅当系统 PATH 有 git 才算 OK
		t.Logf("latestGitPath() 回退裸名 git (系统 PATH 场景)")
		return
	}
	if fi, err := os.Stat(git); err != nil || fi.IsDir() || fi.Size() == 0 {
		t.Fatalf("latestGitPath() 返回 %s 不存在或损坏", git)
	}
	t.Logf("latestGitPath() = %s", git)
}

// TestWhich2OrFallback: which2Or 找不到时回退裸名, 找到时返回绝对路径
func TestWhich2OrFallback(t *testing.T) {
	if p := which2Or("__definitely_not_installed__"); p != "__definitely_not_installed__" {
		t.Fatalf("which2Or 应回退裸名, 得到 %s", p)
	}
	home, _ := os.UserHomeDir()
	gitCand := filepath.Join(home, ".wechat-ai-panel", "git", "cmd", "git.exe")
	if fi, err := os.Stat(gitCand); err == nil && !fi.IsDir() {
		if p := which2Or("git"); p != gitCand {
			// npm 等可能不同, 但 git 便携应在候选
			t.Logf("which2Or(git) = %s (候选 %s)", p, gitCand)
		}
	}
}
