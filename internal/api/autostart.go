// Package api 开机自启管理: /api/autostart (Windows 注册表 Run 键)
//
// 背景: 电脑重启后面板不自动启动 → 服务断链。本模块通过注册表
// HKCU\Software\Microsoft\Windows\CurrentVersion\Run 注册/取消自启。
// Linux/macOS 用户可借助 systemd/launchd (README 说明), 本模块仅 Windows。
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// autostartKey 注册表 Run 键路径
const autostartKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`

// autostartName 注册表值名 (面板标识)
const autostartName = "WeChatAIPanel"

// exeFullPath 面板可执行文件绝对路径 (带引号, 支持特殊字符)
func exeFullPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("定位程序路径失败: %v", err)
	}
	// 加引号防路径含空格
	return `"` + exe + `"`, nil
}

// autostartTarget 注册表要写的命令
func autostartTarget() (string, error) {
	exe, err := exeFullPath()
	if err != nil {
		return "", err
	}
	// 启动后稍等 3 秒 (系统桌面就绪), 后台运行
	return exe + ` --autostart`, nil
}

// AutostartEnabled 查询自启是否已开启
func AutostartEnabled() bool {
	exe, _ := exeFullPath()
	if exe == "" {
		return false
	}
	out, err := exec.Command("reg", "query", autostartKey, "/v", autostartName).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "wechat-ai-panel")
}

// SetAutostart 设置/取消自启 (enable=true 写入 Run 键)
func SetAutostart(enable bool) (string, error) {
	if !enable {
		// 删除
		out, err := exec.Command("reg", "delete", autostartKey, "/v", autostartName, "/f").CombinedOutput()
		if err != nil {
			// 值不存在不算错
			if !strings.Contains(string(out), "ERROR") {
				return "已关闭开机自启", nil
			}
			return "", fmt.Errorf("关闭自启失败: %v\n%s", err, string(out))
		}
		return "已关闭开机自启", nil
	}
	target, err := autostartTarget()
	if err != nil {
		return "", err
	}
	out, err := exec.Command("reg", "add", autostartKey, "/v", autostartName, "/t", "REG_SZ", "/d", target, "/f").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("写入注册表失败: %v\n%s", err, string(out))
	}
	return "已开启开机自启 (下次开机自动启动面板并拉起服务)", nil
}

// RegisterAutostart 注册自启 API
//   - GET  /api/autostart  查询状态 {enabled: bool}
//   - POST /api/autostart  {enabled: true|false} 启用/取消
func (h *Handler) RegisterAutostart() {
	h.mux.HandleFunc("/api/autostart", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			jsonOK(w, map[string]any{
				"ok":      true,
				"enabled": AutostartEnabled(),
				"method":  "registry_run",
			})
		case http.MethodPost:
			if h.authCheck != nil && !h.authCheck(r) {
				jsonErr(w, 401, "未认证或会话已过期")
				return
			}
			var body struct {
				Enabled *bool `json:"enabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Enabled == nil {
				jsonErr(w, 400, "缺少 enabled 字段")
				return
			}
			msg, err := SetAutostart(*body.Enabled)
			if err != nil {
				jsonErr(w, 500, err.Error())
				return
			}
			jsonOK(w, map[string]any{"ok": true, "enabled": AutostartEnabled(), "message": msg})
		default:
			jsonErr(w, 405, "仅支持 GET/POST")
		}
	})
}

// HandleAutostartArg 处理 --autostart 启动参数 (面板以自启方式拉起时, 跳过确认开浏览器等)
func HandleAutostartArg() bool {
	for _, a := range os.Args[1:] {
		if a == "--autostart" {
			return true
		}
	}
	return false
}

// autostartLogDir 自启日志目录 (便于排查)
var autostartLogDir = ""

// SetAutostartLogDir 注入自启日志目录 (main.go)
func SetAutostartLogDir(dir string) { autostartLogDir = dir }

// ensureAutostartLog 确保自启日志目录存在, 返回日志路径
func ensureAutostartLog() string {
	if autostartLogDir == "" {
		return ""
	}
	_ = os.MkdirAll(autostartLogDir, 0o755)
	return filepath.Join(autostartLogDir, "autostart.log")
}

// LogAutostart 追加自启日志
func LogAutostart(line string) {
	p := ensureAutostartLog()
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line + "\n")
	_ = f.Close()
}