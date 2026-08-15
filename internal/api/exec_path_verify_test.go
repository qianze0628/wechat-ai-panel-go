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

// TestWhich2OrNoBareNameFallback: which2Or 找不到时必须返回 ""（不得回退裸名 — 裸名必然
// 重入 exec.Command 的 LookPath 进程 PATH 快照死结）。找到时返回绝对路径。
func TestWhich2OrNoBareNameFallback(t *testing.T) {
	if p := which2Or("__definitely_not_installed__"); p != "" {
		t.Fatalf("which2Or 应返回空串 (禁用裸名回退), 得到 %s", p)
	}
	home, _ := os.UserHomeDir()
	gitCand := filepath.Join(home, ".wechat-ai-panel", "git", "cmd", "git.exe")
	if fi, err := os.Stat(gitCand); err == nil && !fi.IsDir() {
		p := which2Or("git")
		if p == "" {
			t.Fatalf("which2Or(git) 不应为空 — 便携 git 应在候选目录命中")
		}
		t.Logf("which2Or(git) = %s", p)
	}
}
