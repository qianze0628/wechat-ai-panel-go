package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// makeTestZip 生成一个模拟 GitHub 归档结构的 zip (顶层 repo-branch/ + package.json + src/)
func makeTestZip(t *testing.T) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entries := map[string]string{
		"wechat-bot-optimized-main/":                     "",
		"wechat-bot-optimized-main/package.json":         `{"name":"wechat-bot","version":"1.0.0"}`,
		"wechat-bot-optimized-main/cli.js":               "console.log('hi')",
		"wechat-bot-optimized-main/src/index.js":         "export default 1",
		"wechat-bot-optimized-main/src/deep/nested.js":   "// nested",
	}
	for name, content := range entries {
		if strings.HasSuffix(name, "/") {
			if _, err := zw.Create(name); err != nil {
				t.Fatal(err)
			}
			continue
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestExtractZipTo 验证: 解压剥离顶层目录 + 目录结构与 package.json 正确
func TestExtractZipTo(t *testing.T) {
	zipData := makeTestZip(t)
	tmp, err := os.MkdirTemp("", "zip-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	zipPath := filepath.Join(tmp, "repo.zip")
	if err := os.WriteFile(zipPath, zipData, 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(tmp, "out")
	if err := extractZipTo(zipPath, dest); err != nil {
		t.Fatalf("extractZipTo failed: %v", err)
	}
	// package.json 平移到 dest 根
	if _, err := os.Stat(filepath.Join(dest, "package.json")); err != nil {
		t.Error("package.json 未解压到目标根目录")
	}
	// 嵌套目录
	if _, err := os.Stat(filepath.Join(dest, "src", "deep", "nested.js")); err != nil {
		t.Error("嵌套目录未解压")
	}
	// 无顶层残留
	if _, err := os.Stat(filepath.Join(dest, "wechat-bot-optimized-main")); err == nil {
		t.Error("顶层目录未被剥离")
	}
}

// TestFetchRepoZipLocal 验证: 完整下载链路 — 本地 httptest 模拟镜像, zip 正确落盘
func TestFetchRepoZipLocal(t *testing.T) {
	zipData := makeTestZip(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "wechat-bot-optimized") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipData)
	}))
	defer srv.Close()

	// 用本地 server 地址当"镜像前缀" (fetchRepoZip 会拼 archivePath 到其后)
	proxy := srv.URL + "/"
	tmp, err := os.MkdirTemp("", "repo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, "bot")
	ok, msg := fetchRepoZip("https://github.com/qianze0628/wechat-bot-optimized.git", dest, []string{proxy}, nil)
	if !ok {
		t.Fatalf("fetchRepoZip failed: %s", msg)
	}
	if _, err := os.Stat(filepath.Join(dest, "package.json")); err != nil {
		t.Error("下载后 package.json 缺失")
	}
	if _, err := os.Stat(filepath.Join(dest, "cli.js")); err != nil {
		t.Error("下载后 cli.js 缺失")
	}
}

// TestFetchRepoZipFallback 验证: 第一个候选 404/损坏 → 自动回退下一个
func TestFetchRepoZipFallback(t *testing.T) {
	zipData := makeTestZip(t)
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.NotFound(w, r) // 第一个镜像 404
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipData)
	}))
	defer srv.Close()
	// 第一个代理无效 (404), 第二个代理有效 — 应回退成功
	tmp, err := os.MkdirTemp("", "repo-fb-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, "bot")
	proxies := []string{srv.URL + "/bad/", srv.URL + "/"}
	ok, msg := fetchRepoZip("https://github.com/qianze0628/wechat-bot-optimized.git", dest, proxies, nil)
	if !ok {
		t.Fatalf("should fallback to second proxy: %s", msg)
	}
	if attempts < 2 {
		t.Errorf("expected >=2 attempts, got %d", attempts)
	}
}

// TestRepoOwnerName 验证仓库 URL 解析
func TestRepoOwnerName(t *testing.T) {
	cases := []struct{ in, owner, name string }{
		{"https://github.com/qianze0628/wechat-bot-optimized.git", "qianze0628", "wechat-bot-optimized"},
		{"https://github.com/qianze0628/wechat-bot-optimized", "qianze0628", "wechat-bot-optimized"},
		{"https://gh-proxy.com/https://github.com/a/b.git", "a", "b"},
	}
	for _, c := range cases {
		owner, name, err := repoOwnerName(c.in)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.in, err)
			continue
		}
		if owner != c.owner || name != c.name {
			t.Errorf("%s: got %s/%s want %s/%s", c.in, owner, name, c.owner, c.name)
		}
	}
}

// TestRepoZipURLs 验证候选 URL 生成 (镜像在前, 直连兜底)
func TestRepoZipURLs(t *testing.T) {
	urls := repoZipURLs("qianze0628", "wechat-bot-optimized", "main", []string{"https://custom.example/"})
	if len(urls) < 5 {
		t.Fatalf("expected >=5 candidates, got %d", len(urls))
	}
	// 用户镜像最前
	if !strings.HasPrefix(urls[0], "https://custom.example/") {
		t.Errorf("user proxy should be first, got %s", urls[0])
	}
	// 直连最后 (codeload)
	last := urls[len(urls)-1]
	if !strings.HasPrefix(last, "https://codeload.github.com/") {
		t.Errorf("last should be codeload direct, got %s", last)
	}
	// 包含 archive 直连
	foundArchive := false
	for _, u := range urls {
		if u == "https://github.com/qianze0628/wechat-bot-optimized/archive/refs/heads/main.zip" {
			foundArchive = true
		}
	}
	if !foundArchive {
		t.Error("缺少 archive 直连候选")
	}
}

// TestPlanInstallTasksNoGit 验证: 环境检测不再生成 git 安装任务
func TestPlanInstallTasksNoGit(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅在本机验证 git 相关任务移除逻辑")
	}
	platform := detectPlatform()
	tasks := planInstallTasks(platform, filepath.Join(t.TempDir(), "bot"), filepath.Join(t.TempDir(), "root"), "https://github.com/qianze0628/wechat-bot-optimized.git")
	for _, tk := range tasks {
		if tk["kind"] == "env_git" {
			t.Error("不应再生成 env_git 任务 (git 已非必需)")
		}
	}
}

// TestPlanInstallTasksIncludeClone 验证: 缺源码时生成 clone 任务 (zip 下载)
func TestPlanInstallTasksIncludeClone(t *testing.T) {
	platform := detectPlatform()
	wechatDir := filepath.Join(os.TempDir(), "wapanel-test-"+fmt.Sprint(os.Getpid()), "bot")
	defer os.RemoveAll(filepath.Dir(wechatDir))
	tasks := planInstallTasks(platform, wechatDir, filepath.Join(os.TempDir(), "astrbot-root-test"), "https://github.com/qianze0628/wechat-bot-optimized.git")
	foundClone := false
	for _, tk := range tasks {
		if tk["kind"] == "clone" {
			foundClone = true
			if !strings.Contains(tk["label"], "zip") {
				t.Errorf("clone 任务标签应说明 zip 下载, got %s", tk["label"])
			}
		}
	}
	if !foundClone {
		t.Error("缺源码时应生成 clone 任务")
	}
}

// TestDownloadToFileHTMLContent 验证: downloadToFile 对 HTML 错误页 (非 zip 魔数) 拒绝
func TestDownloadToFileHTMLContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>404 Not Found</body></html>"))
	}))
	defer srv.Close()
	tmp, err := os.MkdirTemp("", "repo-html-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, "out.zip")
	err = downloadToFile(newDownloadClient(), srv.URL, dest, 2048)
	if err == nil {
		t.Fatal("HTML 错误页不应通过 downloadToFile 校验")
	}
	// 两种防御任意命中即可: 内容偏小 (错误页短) 或 zip 魔数不匹配
	if !strings.Contains(err.Error(), "zip") && !strings.Contains(err.Error(), "偏小") &&
		!strings.Contains(err.Error(), "异常") {
		t.Errorf("错误信息应说明内容校验失败 (魔数/偏小), got: %v", err)
	}
}

// TestJSON 防未使用导入
func TestJSON(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(`{"a":1}`), &m); err != nil {
		t.Fatal(err)
	}
}
