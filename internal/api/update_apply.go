// Package api 面板自动更新: 下载新版本 → 解压替换 → 重启
// 不跳转 GitHub, 面板内一键完成 (镜像判断在 download-info 已做)
package api

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// updateApplyState 更新执行状态
var updateApplyState = struct {
	Running bool
	Done    bool
	Logs    []string
}{}

// updateApplyLog 追加更新日志
func updateApplyLog(line string) {
	updateApplyState.Logs = append(updateApplyState.Logs, "["+time.Now().Format("15:04:05")+"] "+line)
}

// currentExe 当前运行的程序路径
func currentExe() (string, error) {
	return os.Executable()
}

// assetForPlatform 返回本平台对应的 release asset 名
func assetForPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "wechat-ai-panel-windows-amd64.zip"
	case "linux":
		if runtime.GOARCH == "arm64" {
			return "wechat-ai-panel-linux-arm64.tar.gz"
		}
		return "wechat-ai-panel-linux-amd64.tar.gz"
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "wechat-ai-panel-darwin-arm64.tar.gz"
		}
		return "wechat-ai-panel-darwin-amd64.tar.gz"
	}
	return ""
}

// downloadFile 下载文件到本地 (支持 302 跟随 + 超时)
func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "wechat-ai-panel-auto-update")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载失败 HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	// 流式复制 (进度由前端轮询日志感知)
	_, err = io.Copy(out, resp.Body)
	return err
}

// extractArchive 解压 zip/tar.gz 到目录, 返回内部可执行文件名
func extractArchive(archivePath, destDir string) (string, error) {
	os.MkdirAll(destDir, 0o755)
	if strings.HasSuffix(archivePath, ".zip") {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return "", err
		}
		defer zr.Close()
		var exeName string
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			target := filepath.Join(destDir, filepath.Base(f.Name))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			out, err := os.Create(target)
			if err != nil {
				rc.Close()
				return "", err
			}
			_, _ = io.Copy(out, rc)
			out.Close()
			rc.Close()
			// windows 的 exe 名
			if strings.HasSuffix(f.Name, ".exe") && exeName == "" {
				exeName = target
			}
		}
		if exeName == "" {
			return "", fmt.Errorf("zip 内未找到 .exe")
		}
		return exeName, nil
	}
	// tar.gz
	gr, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer gr.Close()
	gzr, err := gzip.NewReader(gr)
	if err != nil {
		return "", err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	var binName string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		target := filepath.Join(destDir, filepath.Base(hdr.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		out, err := os.Create(target)
		if err != nil {
			return "", err
		}
		_, _ = io.Copy(out, tr)
		out.Close()
		if binName == "" && (hdr.Name == "wechat-ai-panel" || strings.HasSuffix(hdr.Name, "/wechat-ai-panel")) {
			binName = target
		}
	}
	if binName == "" {
		return "", fmt.Errorf("未在归档中找到可执行文件")
	}
	return binName, nil
}

// applyUpdate 执行自动更新 (下载→解压→替换→返回重启命令)
func applyUpdate(version string) (string, error) {
	asset := assetForPlatform()
	if asset == "" {
		return "", fmt.Errorf("不支持当前平台: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	// 1. 下载信息 (含镜像判断)
	info := DownloadInfo{}
	direct := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", GH, version, asset)
	region := detectRegion()
	info.Region = region
	info.DirectURL = direct
	info.FinalURL = direct
	if region == "CN" {
		info.UseMirror = true
		info.MirrorPrefix = "https://gh-proxy.com/"
		info.FinalURL = info.MirrorPrefix + direct
	}

	// 2. 临时目录
	tmpDir, err := os.MkdirTemp("", "wapanel-update-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)
	archivePath := filepath.Join(tmpDir, asset)

	// 3. 下载 (先镜像, 失败回退直连)
	updateApplyLog(fmt.Sprintf("下载中: %s (region=%s mirror=%v)", asset, region, info.UseMirror))
	if err := downloadFile(info.FinalURL, archivePath); err != nil {
		if info.UseMirror {
			updateApplyLog(fmt.Sprintf("镜像下载失败(%v), 回退直连", err))
			info.FinalURL = info.DirectURL
			if err2 := downloadFile(info.FinalURL, archivePath); err2 != nil {
				return "", fmt.Errorf("下载失败: %v", err2)
			}
		} else {
			return "", fmt.Errorf("下载失败: %v", err)
		}
	}
	updateApplyLog("下载完成, 解压中...")

	// 4. 解压
	binName, err := extractArchive(archivePath, tmpDir)
	if err != nil {
		return "", fmt.Errorf("解压失败: %v", err)
	}

	// 5. 替换当前 exe (先备份旧版)
	exe, err := currentExe()
	if err != nil {
		return "", err
	}
	backup := exe + ".v"+ currentVersionShort() + ".bak"
	_ = os.Rename(exe, backup) // 备份旧版
	if err := os.Rename(binName, exe); err != nil {
		// 替换失败回滚
		_ = os.Rename(backup, exe)
		return "", fmt.Errorf("替换失败: %v", err)
	}
	updateApplyLog(fmt.Sprintf("已替换 %s (备份 %s)", exe, backup))

	// 6. 重启面板 (守护线程会拉起服务)
	updateApplyLog("更新完成, 面板将自动重启")
	go func() {
		time.Sleep(500 * time.Millisecond)
		exe2, _ := os.Executable()
		cmd := exec.Command(exe2)
		cmd.Start()
		os.Exit(0)
	}()
	return "更新成功, 面板即将重启", nil
}

func currentVersionShort() string {
	return versionTag // 由 main.go 注入的版本号
}

// versionTag 当前面板版本 (main.go 注入; /api/status 也用它, 单一来源)
var versionTag = "v0.2.2"

// SetVersionTag 注入当前版本
func SetVersionTag(v string) { versionTag = v }

// VersionTag 返回当前面板版本 (供 /api/status 使用)
func VersionTag() string { return versionTag }

// assetForUpdate 别名 (对齐 update.go 的 assetForPlatform)
func assetForUpdate() string { return assetForPlatform() }

// RegisterUpdateApply 注册自动更新 API
//   - POST /api/update/apply {"version":"v0.2.0"} 执行自动更新
//   - GET  /api/update/apply/status  查询更新状态/日志
func (h *Handler) RegisterUpdateApply() {
	h.mux.HandleFunc("/api/update/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonErr(w, 405, "仅支持 POST")
			return
		}
		if h.authCheck != nil && !h.authCheck(r) {
			jsonErr(w, 401, "未认证或会话已过期")
			return
		}
		var body struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
			jsonErr(w, 400, "缺少 version 字段")
			return
		}
		updateApplyState.Done = false
		updateApplyState.Logs = nil
		msg, err := applyUpdate(body.Version)
		if err != nil {
			updateApplyState.Done = true
			jsonOK(w, map[string]any{"ok": false, "message": err.Error()})
			return
		}
		jsonOK(w, map[string]any{"ok": true, "message": msg})
	})
	h.mux.HandleFunc("/api/update/apply/status", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{"done": updateApplyState.Done, "logs": updateApplyState.Logs})
	})
}