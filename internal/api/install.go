// Package api 安装引擎: /api/install (多平台) + /api/install/status
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"wechat-ai-panel/internal/config"
)

// InstallState 安装状态 (单例)
type InstallState struct {
	mu       sync.Mutex
	Running  bool     `json:"running"`
	Logs     []string `json:"logs"`
	Done     bool     `json:"done"`
	OK       *bool    `json:"ok"`
	Platform string   `json:"platform"`
	Where    map[string]any `json:"install_where"`
}

var installState = &InstallState{OK: nil}

// detectPlatform 检测当前平台
func detectPlatform() string {
	if runtime.GOOS == "windows" {
		return "windows"
	}
	if runtime.GOOS == "darwin" {
		return "mac"
	}
	return "linux"
}

// planInstallTasks 规划安装任务
func planInstallTasks(platform, wechatDir, astrbotRoot string) []map[string]string {
	var tasks []map[string]string
	pkg := filepath.Join(wechatDir, "package.json")
	nm := filepath.Join(wechatDir, "node_modules")
	if _, err := os.Stat(pkg); err == nil {
		if _, err := os.Stat(nm); err != nil {
			tasks = append(tasks, map[string]string{
				"label": "npm install (wechat-bot @ " + wechatDir + ")",
				"kind":  "npm", "target": wechatDir,
			})
		}
	} else {
		tasks = append(tasks, map[string]string{
			"label": "wechat-bot 源码缺失: " + wechatDir, "kind": "warn", "target": wechatDir,
		})
	}
	if which2("astrbot") == "" {
		tasks = append(tasks, map[string]string{
			"label": "uv tool install astrbot", "kind": "uv", "target": astrbotRoot,
		})
	}
	return tasks
}

// runInstall 后台执行安装
func runInstall(tasks []map[string]string, platform, wechatDir, astrbotRoot string) {
	installState.mu.Lock()
	ok := true
	installState.Running = true
	installState.Done = false
	installState.Platform = platform
	installState.Logs = nil
	installState.Where = map[string]any{
		"platform": platform, "wechat_dir": wechatDir, "astrbot_dir": astrbotRoot,
	}
	installState.mu.Unlock()

	for _, t := range tasks {
		installState.mu.Lock()
		installState.Logs = append(installState.Logs, "["+platform+"] [start] "+t["label"])
		installState.mu.Unlock()
		var cmd *exec.Cmd
		switch t["kind"] {
		case "npm":
			cmd = exec.Command("npm", "install")
			cmd.Dir = t["target"]
		case "uv":
			cmd = exec.Command("uv", "tool", "install", "astrbot")
		}
		if cmd != nil {
			cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
			done := make(chan error, 1)
			go func() { done <- cmd.Run() }()
			select {
			case err := <-done:
				msg := "[done] " + t["label"]
				if err != nil {
					ok = false
					msg += " FAILED: " + err.Error()
				} else {
					msg += " exit=0"
				}
				installState.mu.Lock()
				installState.Logs = append(installState.Logs, msg)
				installState.mu.Unlock()
			case <-time.After(900 * time.Second):
				ok = false
				cmd.Process.Kill()
				installState.mu.Lock()
				installState.Logs = append(installState.Logs, "["+platform+"] [error] "+t["label"]+" 超时")
				installState.mu.Unlock()
			}
		} else {
			installState.mu.Lock()
			installState.Logs = append(installState.Logs, "["+platform+"] [warn] "+t["label"])
			installState.mu.Unlock()
		}
	}
	installState.mu.Lock()
	installState.Running = false
	installState.Done = true
	installState.OK = &ok
	installState.mu.Unlock()
}

// RegisterInstall 注册安装 API
func (h *Handler) RegisterInstall(cfg *config.Config) {
	h.HandleFunc("/api/install", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonErr(w, 405, "仅支持 POST")
			return
		}
		platform := detectPlatform()
		wechatDir := cfg.WechatBotDir
		astrbotRoot := cfg.AstrbotRoot
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if p, ok := body["platform"].(string); ok && p != "" {
			platform = p
		}
		if d, ok := body["wechat_dir"].(string); ok && d != "" {
			wechatDir = d
		}
		if d, ok := body["astrbot_dir"].(string); ok && d != "" {
			astrbotRoot = d
		}
		if platform != "windows" && platform != "mac" && platform != "linux" {
			jsonErr(w, 400, "未知平台: "+platform)
			return
		}
		tasks := planInstallTasks(platform, wechatDir, astrbotRoot)
		if len(tasks) == 0 {
			jsonOK(w, map[string]any{
				"ok": true, "message": "所有组件已就绪, 无需安装", "tasks": []any{},
				"platform": platform, "wechat_dir": wechatDir, "astrbot_dir": astrbotRoot,
			})
			return
		}
		// 后台执行 (避免重复)
		installState.mu.Lock()
		if installState.Running {
			installState.mu.Unlock()
			jsonOK(w, map[string]any{"ok": true, "message": "安装已在后台进行中"})
			return
		}
		installState.mu.Unlock()
		go runInstall(tasks, platform, wechatDir, astrbotRoot)
		labels := make([]string, 0, len(tasks))
		for _, t := range tasks {
			labels = append(labels, t["label"])
		}
		jsonOK(w, map[string]any{
			"ok": true, "message": "开始安装 (" + platform + "): " + joinStr(labels),
			"tasks": tasks, "platform": platform, "wechat_dir": wechatDir, "astrbot_dir": astrbotRoot,
		})
	})

	h.HandleFunc("/api/install/status", func(w http.ResponseWriter, r *http.Request) {
		installState.mu.Lock()
		defer installState.mu.Unlock()
		jsonOK(w, map[string]any{
			"running": installState.Running, "logs": installState.Logs,
			"done": installState.Done, "ok": installState.OK,
			"platform": installState.Platform, "install_where": installState.Where,
		})
	})
}

func joinStr(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " / "
		}
		out += x
	}
	return out
}