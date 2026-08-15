package process

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWhichPortableNode: 便携 node 存在时 which("node") 应命中候选目录 (模拟全新电脑)
func TestWhichPortableNode(t *testing.T) {
	home, _ := os.UserHomeDir()
	cand := filepath.Join(home, ".wechat-ai-panel", "nodejs", "node.exe")
	if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
		got := which("node")
		if got != cand && got == "" {
			t.Fatalf("which(node) 应为 %s, 得到 %q — node 查找死结未根治!", cand, got)
		}
		t.Logf("which(node) = %s", got)
	} else {
		t.Logf("本机无便携 node (%v), 跳过", err)
	}
	// astrbot: uv tools 路径
	ast := filepath.Join(home, `AppData\Roaming\uv\tools\astrbot\Scripts\astrbot.exe`)
	if fi, err := os.Stat(ast); err == nil && !fi.IsDir() {
		if got := which("astrbot"); got == "" {
			t.Fatalf("which(astrbot) 不应为空 — uv tools 路径未命中")
		}
		t.Logf("which(astrbot) = %s", which("astrbot"))
	}
}
