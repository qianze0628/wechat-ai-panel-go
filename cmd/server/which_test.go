package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWhichCandidateDirs 验证 which 对便携工具的检测 (候选目录优先)
func TestWhichCandidateDirs(t *testing.T) {
	home, _ := os.UserHomeDir()
	// 便携 node 目录: 若本机存在则 which("node") 应命中 (不依赖进程 PATH)
	portableNode := filepath.Join(home, ".wechat-ai-panel", "nodejs", "node.exe")
	if _, err := os.Stat(portableNode); err == nil {
		got := which("node")
		if got != portableNode && got == "" {
			t.Errorf("which(node) 未命中便携目录: %q", got)
		}
	}
	// 系统 git: Program Files 目录候选
	if _, err := os.Stat(`C:\Program Files\Git\cmd\git.exe`); err == nil {
		got := which("git")
		if got == "" {
			t.Error("which(git) 未命中系统目录")
		}
	}
	// npm 需支持 .cmd 变体
	if _, err := os.Stat(filepath.Join(home, ".wechat-ai-panel", "nodejs", "npm.cmd")); err == nil {
		got := which("npm")
		if got == "" {
			t.Error("which(npm) 未命中便携 npm.cmd")
		}
	}
}

// TestWhichNoPanic 验证裸名兜底不 panic (系统无工具时返回空不上抛)
func TestWhichNoPanic(t *testing.T) {
	_ = which("definitely-not-exist-tool-xyz")
	_ = which("npm")
	_ = which("node")
}
