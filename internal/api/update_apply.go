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

// assetForPlatform 返回本平台对应的 release asset 名 (用目标版本号构造)
// 修复 (2026-08-11): release 资产名是 wechat-ai-panel_<version>_<os>_<arch>.exe (带版本号, 无 zip),
// 之前返回 wechat-ai-panel-windows-amd64.zip → 下载 URL 404 → 面板更新失败。
// 修复 (2026-08-11 agent 审查): 版本号必须用**目标更新版本**, 不能用当前运行版本
// (从 v0.5.2 更新到 v0.5.3 时, 用当前版 v0.5.2 构造资产名 → v0.5.3/wechat-ai-panel_0.5.2... → 404)
func assetForPlatform() string {
	return assetForVersion(VersionTag())
}

// assetForVersion 用指定版本构造资产名 (目标版本来自 applyUpdate 的 version 参数)
func assetForVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	switch runtime.GOOS {
	case "windows":
		return "wechat-ai-panel_" + v + "_windows_amd64.exe"
	case "linux":
		arch := "amd64"
		if runtime.GOARCH == "arm64" {
			arch = "arm64"
		}
		return "wechat-ai-panel_" + v + "_linux_" + arch
	case "darwin":
		arch := "amd64"
		if runtime.GOARCH == "arm64" {
			arch = "arm64"
		}
		return "wechat-ai-panel_" + v + "_darwin_" + arch
	}
	return ""
}

// copyFile 复制文件 (用于裸 exe 资产)
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	cerr := out.Close()
	if err != nil {
		return err
	}
	return cerr
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
// 修复 (2026-08-11): release 资产是裸 exe (无 zip/tar.gz) → 直接复制, 不走到 tar.gz 分支报错
func extractArchive(archivePath, destDir string) (string, error) {
	os.MkdirAll(destDir, 0o755)
	if !strings.HasSuffix(archivePath, ".zip") && !strings.HasSuffix(archivePath, ".tar.gz") && !strings.HasSuffix(archivePath, ".tgz") {
		// 裸可执行文件 (release 资产 = wechat-ai-panel_<v>_<os>_<arch>.exe)
		target := filepath.Join(destDir, filepath.Base(archivePath))
		if err := copyFile(archivePath, target); err != nil {
			return "", err
		}
		_ = os.Chmod(target, 0o755)
		return target, nil
	}
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
	// 修复 (2026-08-11 agent 审查): 资产名必须用**目标版本**构造
	// (assetForPlatform() 用当前版本 → 从旧版更新时 URL 404)
	asset := assetForVersion(version)
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

	// 5. 替换当前 exe (Windows 上运行中 exe 被锁定无法重命名 → 延迟脚本方案)
	// 修复 (2026-08-11 agent 审查 P1-4): 之前 os.Rename(exe, backup) 在 Windows 必然失败
	// (进程自身 exe 句柄锁定) → 更新永远失败。改为: 新 exe 放旁侧 → 写重启 bat
	// (延迟 1s → copy /Y 替换 → 启动新 exe → 清理) → 启动 bat 后本进程退出。
	exe, err := currentExe()
	if err != nil {
		return "", err
	}
	exeDir := filepath.Dir(exe)
	// 新 exe 放 exe 同目录的临时名 (bat 需要从同盘复制; 避免 tmpDir 被 defer 删除)
	newExe := filepath.Join(exeDir, ".wechat-ai-panel-new.exe")
	if err := copyFile(binName, newExe); err != nil {
		return "", fmt.Errorf("暂存新版本失败: %v", err)
	}
	backup := exe + ".v" + currentVersionShort() + ".bak"
	updateApplyLog(fmt.Sprintf("已下载新版本, 通过重启脚本替换 (备份 → %s)", backup))

	// 6. 写重启 bat (延迟替换 + 启动新 exe + 清理)
	batPath := filepath.Join(exeDir, ".wechat-ai-panel-restart.bat")
	bat := "@echo off\r\n" +
		"ping 127.0.0.1 -n 2 >nul\r\n" + // 等待本进程完全退出
		`if exist "` + backup + `" del /q "` + backup + `"` + "\r\n" +
		`if exist "` + exe + `" ren "` + exe + `" "` + filepath.Base(backup) + `"` + "\r\n" +
		`copy /Y "` + newExe + `" "` + exe + `"` + "\r\n" +
		`del /q "` + newExe + `"` + "\r\n" +
		`start "" "` + exe + `"` + "\r\n" +
		`del /q "` + batPath + `"` + "\r\n"
	if err := os.WriteFile(batPath, []byte(bat), 0o644); err != nil {
		return "", fmt.Errorf("写重启脚本失败: %v", err)
	}

	// 7. 启动 bat 后本进程退出 (bat 会在 1s 后替换并重启面板)
	// 修复 (2026-08-11 agent 审查 P2-2): 用 start /b 独立启动 bat, 避免 os.Exit 杀掉 cmd 子进程
	updateApplyLog("更新完成, 面板将自动重启")
	exec.Command("cmd", "/c", "start", "/b", "", batPath).Start()
	go func() {
		time.Sleep(1500 * time.Millisecond)
		os.Exit(0)
	}()
	return "更新成功, 面板即将重启", nil
}

func currentVersionShort() string {
	return versionTag // 由 main.go 注入的版本号
}

// versionTag 当前面板版本 (main.go 注入; /api/status 也用它, 单一来源)
var versionTag = "v0.5.1"

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