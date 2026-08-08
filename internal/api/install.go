// Package api 安装引擎: /api/install (多平台) + /api/install/status
package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// planInstallTasks 规划安装任务 (wechatRepo 非空时, 缺源码自动 git clone 优化版)
func planInstallTasks(platform, wechatDir, astrbotRoot, wechatRepo string) []map[string]string {
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
		if wechatRepo != "" {
			tasks = append(tasks, map[string]string{
				"label": "git clone " + wechatRepo + " → " + wechatDir,
				"kind":  "clone", "target": wechatDir, "repo": wechatRepo,
			})
		} else {
			tasks = append(tasks, map[string]string{
				"label": "wechat-bot 源码缺失: " + wechatDir, "kind": "warn", "target": wechatDir,
			})
		}
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
		case "clone":
			// wechat-bot 优化版源码: git clone --depth 1, 之后自动 npm install
			_ = os.MkdirAll(t["target"], 0o755)
			cmd = exec.Command("git", "clone", "--depth", "1", t["repo"], t["target"])
		}
		if cmd != nil {
			cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
			// 实时捕获 stdout/stderr 到 installState.Logs (让前端能看到安装进度)
			stdout, _ := cmd.StdoutPipe()
			stderr, _ := cmd.StderrPipe()
			if stdout != nil {
				go func() {
					sc := bufio.NewScanner(stdout)
					sc.Buffer(make([]byte, 64*1024), 1024*1024)
					for sc.Scan() {
						line := strings.TrimRight(sc.Text(), "\r")
						if line == "" {
							continue
						}
						installState.mu.Lock()
						installState.Logs = append(installState.Logs, line)
						installState.mu.Unlock()
					}
				}()
			}
			if stderr != nil {
				go func() {
					sc := bufio.NewScanner(stderr)
					sc.Buffer(make([]byte, 64*1024), 1024*1024)
					for sc.Scan() {
						line := strings.TrimRight(sc.Text(), "\r")
						if line == "" {
							continue
						}
						installState.mu.Lock()
						installState.Logs = append(installState.Logs, line)
						installState.mu.Unlock()
					}
				}()
			}
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
				// clone 成功后自动 npm install
				if t["kind"] == "clone" && err == nil {
					installState.mu.Lock()
					installState.Logs = append(installState.Logs, "["+platform+"] [start] npm install (wechat-bot)")
					installState.mu.Unlock()
					nmCmd := exec.Command("npm", "install")
					nmCmd.Dir = t["target"]
					nmCmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
					d2 := make(chan error, 1)
					go func() { d2 <- nmCmd.Run() }()
					var msg2 string
					select {
					case err2 := <-d2:
						if err2 != nil {
							ok = false
							msg2 = "[done] npm install FAILED: " + err2.Error()
						} else {
							msg2 = "[done] npm install exit=0"
						}
					case <-time.After(600 * time.Second):
						ok = false
						nmCmd.Process.Kill()
						msg2 = "[" + platform + "] [error] npm install 超时"
					}
					installState.mu.Lock()
					installState.Logs = append(installState.Logs, msg2)
					installState.mu.Unlock()
				}
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
		wechatRepo := cfg.WechatBotRepo
		// 前端可能传 platform (浏览器系统), 但安装必须基于面板实际运行系统
		// (runtime.GOOS)。前端传的值仅在校验通过时作为提示, 不覆盖真实平台。
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if p, ok := body["platform"].(string); ok && p != "" {
			// 仅在面板真实平台无效时 (理论上不会) 使用前端值; 否则以 detectPlatform 为准
			_ = p
		}
		if d, ok := body["wechat_dir"].(string); ok && d != "" {
			wechatDir = d
		}
		if d, ok := body["astrbot_dir"].(string); ok && d != "" {
			astrbotRoot = d
		}
		if d, ok := body["wechat_repo"].(string); ok && d != "" {
			wechatRepo = strings.TrimSpace(d)
		}
		if platform != "windows" && platform != "mac" && platform != "linux" {
			jsonErr(w, 400, "未知平台: "+platform)
			return
		}
		tasks := planInstallTasks(platform, wechatDir, astrbotRoot, wechatRepo)
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
		logs := installState.Logs
		if logs == nil {
			logs = []string{}
		}
		jsonOK(w, map[string]any{
			"running": installState.Running, "logs": logs,
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