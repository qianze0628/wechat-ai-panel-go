// Package api 源码获取模块: 以 HTTP zip 方式下载 GitHub 仓库源码 (零外部依赖)。
// 背景 (2026-08-15 第一性原理重构): 旧实现用 exec.Command("git", "clone", ...) — 裸命令名
// 依赖面板进程 PATH 快照解析 git 二进制, 朋友电脑无 git 时必报
//   exec: "git": executable file not found in %PATH%
// 挂代理也无法解决 (根本不是网络问题, 是二进制不存在)。
// 第一性原理: 拉源码只需要"仓库文件内容", 不需要 git 版本控制能力 → GitHub 官方支持
//   zip 归档下载 (codeload), Go 标准库 net/http + archive/zip 即可完成, 零外部二进制依赖。
// 镜像策略: gh-proxy 等公共加速服务对 github.com 的任意 URL 前缀同样有效,
//   所以 zip URL 也可走镜像 (https://gh-proxy.com/https://github.com/.../archive/refs/heads/main.zip)。
package api

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// repoZipBranchCandidates 分支探测顺序 (default_branch 探测失败时依次尝试)。
// 仓库默认分支通常是 main (新) 或 master (旧), 404 时自动切换。
var repoZipBranchCandidates = []string{"main", "master"}

// builtinZipProxies 内置镜像前缀 (对 github.com 任意 URL 前缀加速), 顺序即尝试顺序。
var builtinZipProxies = []string{
	"https://gh-proxy.com/",
	"https://ghfast.top/",
	"https://ghproxy.net/",
	"https://mirror.ghproxy.com/",
}

// repoOwnerName 从仓库 URL 解析 owner/name (支持 https://github.com/user/repo[.git])
func repoOwnerName(repo string) (owner, name string, err error) {
	r := strings.TrimSpace(repo)
	r = strings.TrimSuffix(r, ".git")
	r = strings.TrimRight(r, "/")
	if idx := strings.Index(r, "github.com/"); idx >= 0 {
		r = r[idx+len("github.com/"):]
	}
	parts := strings.Split(r, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("无法解析仓库 URL: %s", repo)
	}
	owner = parts[len(parts)-2]
	name = parts[len(parts)-1]
	if owner == "" || name == "" {
		return "", "", fmt.Errorf("仓库 URL 缺少 owner/name: %s", repo)
	}
	return owner, name, nil
}

// repoZipURLs 生成候选下载 URL 列表 (镜像先行, 直连兜底)。
// proxies 为额外镜像前缀 (来自配置 git_clone_proxy), 追加到内置镜像之前。
func repoZipURLs(owner, name, branch string, proxies []string) []string {
	archivePath := fmt.Sprintf("https://github.com/%s/%s/archive/refs/heads/%s.zip", owner, name, branch)
	codeloadPath := fmt.Sprintf("https://codeload.github.com/%s/%s/zip/refs/heads/%s", owner, name, branch)
	var urls []string
	allProxies := append(append([]string{}, proxies...), builtinZipProxies...)
	for _, p := range allProxies {
		p = strings.TrimSuffix(p, "/") + "/"
		urls = append(urls, p+archivePath)
	}
	urls = append(urls, archivePath)
	urls = append(urls, codeloadPath)
	return urls
}

// downloadToFile 下载 URL 到本地文件 (流式, 校验 zip 魔数防 HTML 错误页)
func downloadToFile(client *http.Client, url, dest string, minSize int64) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "wechat-ai-panel-installer")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, resp.Body)
	if err != nil {
		return err
	}
	if n < minSize {
		return fmt.Errorf("下载内容异常偏小 (%d bytes), 疑似错误页", n)
	}
	// 校验 zip 魔数 PK\x03\x04 (防止下载到 HTML 错误页)
	if _, err := out.Seek(0, 0); err != nil {
		return err
	}
	head := make([]byte, 4)
	if _, err := out.Read(head); err != nil {
		return err
	}
	if !(head[0] == 'P' && head[1] == 'K' && head[2] == 0x03 && head[3] == 0x04) {
		return fmt.Errorf("下载内容非 zip 归档 (魔数异常), 可能被镜像返回错误页")
	}
	return nil
}

// extractZipTo 解压 zip 到 destDir, 剥离顶层单目录 (GitHub 归档顶层是 repo-branch/),
// 并把内容平移到 destDir 根。防路径穿越。
func extractZipTo(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) == 0 {
		return fmt.Errorf("zip 为空")
	}
	first := zr.File[0].Name
	topPrefix := ""
	if strings.HasSuffix(first, "/") {
		topPrefix = first
	} else if idx := strings.Index(first, "/"); idx >= 0 {
		topPrefix = first[:idx+1]
	}
	for _, f := range zr.File {
		name := f.Name
		if topPrefix != "" && strings.HasPrefix(name, topPrefix) {
			name = strings.TrimPrefix(name, topPrefix)
		}
		if name == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(name))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("zip 含非法路径: %s", name)
		}
		target := filepath.Join(destDir, clean)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

// fetchRepoZip 下载并解压仓库 zip 到 destDir, 校验标志文件为 package.json
// (wechat-bot 源码场景)。等价于 fetchRepoZipWithMarker(repo, destDir, "package.json", proxies, logf)。
func fetchRepoZip(repo, destDir string, proxies []string, logf func(format string, args ...any)) (bool, string) {
	return fetchRepoZipWithMarker(repo, destDir, "package.json", proxies, logf)
}

// fetchRepoZipWithMarker 下载并解压仓库 zip 到 destDir (替换旧 git clone 链路)。
// marker 非空时要求解压结果中存在该文件名 (如 package.json);
// marker 为空时只要解压出任意非空文件即成功 (插件等无固定标志文件的场景)。
// 返回 (成功与否, 人类可读错误)。destDir 会被清空重建。
// logf 为可选的进度回调 (可为 nil), 用于把尝试的 URL 实时写入安装日志。
func fetchRepoZipWithMarker(repo, destDir, marker string, proxies []string, logf func(format string, args ...any)) (bool, string) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	owner, name, err := repoOwnerName(repo)
	if err != nil {
		return false, err.Error()
	}
	logf("[zip] 仓库: %s/%s (分支探测: %v)", owner, name, strings.Join(repoZipBranchCandidates, ","))
	client := newDownloadClient()
	tmpZip, err := os.CreateTemp("", "repo-*.zip")
	if err != nil {
		return false, "创建临时文件失败: " + err.Error()
	}
	tmpZipPath := tmpZip.Name()
	tmpZip.Close()
	defer os.Remove(tmpZipPath)

	tmpDir, err := os.MkdirTemp("", "repo-extract-*")
	if err != nil {
		return false, "创建临时目录失败: " + err.Error()
	}
	defer os.RemoveAll(tmpDir)

	var lastErr string
	for _, branch := range repoZipBranchCandidates {
		urls := repoZipURLs(owner, name, branch, proxies)
		for _, u := range urls {
			logf("[zip] 尝试下载: %s", shortenURL(u))
			if err := downloadToFile(client, u, tmpZipPath, 2048); err != nil {
				lastErr = fmt.Sprintf("下载失败 (%s): %v", shortenURL(u), err)
				logf("[zip] [warn] %s", lastErr)
				continue
			}
			if err := extractZipTo(tmpZipPath, tmpDir); err != nil {
				lastErr = fmt.Sprintf("解压失败 (%s): %v", shortenURL(u), err)
				continue
			}
			okPkg := false
			if marker != "" {
				_ = filepath.Walk(tmpDir, func(p string, info os.FileInfo, err error) error {
					if err == nil && !info.IsDir() && info.Name() == marker {
						okPkg = true
					}
					return nil
				})
				if !okPkg {
					lastErr = fmt.Sprintf("归档内未找到 %s (%s)", marker, shortenURL(u))
					logf("[zip] [warn] %s", lastErr)
					_ = os.RemoveAll(tmpDir)
					_ = os.MkdirAll(tmpDir, 0o755)
					continue
				}
			} else {
				// 无标志文件: 解压出任意非空内容即认为成功
				nonEmpty := false
				_ = filepath.Walk(tmpDir, func(p string, info os.FileInfo, err error) error {
					if err == nil && !info.IsDir() && info.Size() > 0 {
						nonEmpty = true
					}
					return nil
				})
				if !nonEmpty {
					lastErr = fmt.Sprintf("归档内容为空 (%s)", shortenURL(u))
					_ = os.RemoveAll(tmpDir)
					_ = os.MkdirAll(tmpDir, 0o755)
					continue
				}
			}
			_ = os.RemoveAll(destDir)
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				return false, "创建目标目录失败: " + err.Error()
			}
			if err := copyDirTree(tmpDir, destDir); err != nil {
				return false, "移动源码失败: " + err.Error()
			}
			return true, ""
		}
	}
	return false, "所有下载源均失败: " + lastErr
}

// copyDirTree 递归复制目录 (src 内容 → dst)
func copyDirTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			if err := copyDirTree(s, d); err != nil {
				return err
			}
			continue
		}
		if err := copyOneFile(s, d); err != nil {
			return err
		}
	}
	return nil
}

func copyOneFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// shortenURL 缩短日志中的 URL (截断过长镜像前缀)
func shortenURL(u string) string {
	if len(u) > 140 {
		return u[:140] + "..."
	}
	return u
}