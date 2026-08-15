package api

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFetchRepoZipRealGitHub 真实网络端到端: 从 GitHub 下载 wechat-bot-optimized 源码。
// 需要外网可达; 不可达时跳过 (CI/离线环境不阻断)。
func TestFetchRepoZipRealGitHub(t *testing.T) {
	if os.Getenv("SKIP_NET_TESTS") == "1" {
		t.Skip("SKIP_NET_TESTS=1")
	}
	dest := filepath.Join(os.TempDir(), "wapanel-e2e-bot")
	_ = os.RemoveAll(dest)
	ok, msg := fetchRepoZip("https://github.com/qianze0628/wechat-bot-optimized.git", dest, nil, nil)
	defer os.RemoveAll(dest)
	if !ok {
		t.Skipf("外网不可达或仓库不可访问: %s", msg)
	}
	if _, err := os.Stat(filepath.Join(dest, "package.json")); err != nil {
		t.Fatalf("下载成功但 package.json 缺失: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "cli.js")); err != nil {
		t.Errorf("cli.js 缺失")
	}
	// src 目录
	if _, err := os.Stat(filepath.Join(dest, "src")); err != nil {
		t.Errorf("src 目录缺失")
	}
	t.Logf("真实下载 OK: %s", dest)
}
