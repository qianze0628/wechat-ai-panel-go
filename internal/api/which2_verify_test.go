package api

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWhich2LocalPortable: 本机便携 git/node 应在候选目录命中
// (模拟"MinGit 解压成功但 PATH 缓存死结导致 which2 miss"场景)
func TestWhich2LocalPortable(t *testing.T) {
	home, _ := os.UserHomeDir()
	gitCand := filepath.Join(home, ".wechat-ai-panel", "git", "cmd", "git.exe")
	if fi, err := os.Stat(gitCand); err == nil && !fi.IsDir() {
		if got := which2("git"); got == "" {
			t.Fatalf("which2(git) 应为 %s — 便携 git 检测失败 (PATH缓存死结未根治!)", gitCand)
		}
	} else {
		t.Logf("本机无便携 git (%v), 跳过 git 检查", err)
	}
	nodeCand := filepath.Join(home, ".wechat-ai-panel", "nodejs", "node.exe")
	if fi, err := os.Stat(nodeCand); err == nil && !fi.IsDir() {
		if got := which2("node"); got == "" {
			t.Fatalf("which2(node) 应为 %s — 便携 node 检测失败", nodeCand)
		}
	} else {
		t.Logf("本机无便携 node (%v), 跳过 node 检查", err)
	}
}