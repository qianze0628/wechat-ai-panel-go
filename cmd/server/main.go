// 微信 AI 机器人管理面板 — Go 版 (refactor)
// 目标: 原生 exe + 内嵌 React 前端 + 轻量进程管理
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"wechat-ai-panel/internal/api"
	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/process"
)

//go:embed all:web
var webFS embed.FS

// web 子目录静态资源
var webSub, _ = fs.Sub(webFS, "web")

func main() {
	// 程序运行目录 (exe 所在)
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("定位程序路径失败: %v", err)
	}
	baseDir := filepath.Dir(exePath)
	// 开发模式: 用源码目录 (go run)
	if _, err := os.Stat(filepath.Join(baseDir, "config.json")); err != nil {
		if cwd, e := os.Getwd(); e == nil {
			baseDir = cwd
		}
	}

	cfg := config.Default()
	if err := cfg.Load(baseDir); err != nil {
		log.Printf("配置校验告警: %v", err)
	}

	// 聚合 /api/status 状态
	statusFn := func() any {
		return map[string]any{
			"ok":      true,
			"version": "go-skeleton",
			"env": map[string]any{
				"node":   which("node"),
				"npm":    which("npm"),
				"uv":     which("uv"),
				"python": which("python"),
			},
			"services": serviceStatus(&cfg),
			"config": map[string]any{
				"wechat_bot_dir": cfg.WechatBotDir,
				"astrbot_root":   cfg.AstrbotRoot,
				"cmd_config":     cfg.Astrbot.CmdConfig,
				"config_errors":  cfg.ConfigErrors,
			},
		}
	}

	srv := api.New(webSub)
	srv.SetStatusHandler(statusFn)

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	log.Printf("[panel] 微信 AI 管理面板 (Go): http://localhost:%d", cfg.Port)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("监听失败: %v", err)
	}
}

// serviceStatus 服务运行状态
func serviceStatus(cfg *config.Config) map[string]any {
	return map[string]any{
		"astrbot": map[string]any{
			"running":    process.PortListening(cfg.Services.Astrbot.WebUIPort),
			"webui_port": cfg.Services.Astrbot.WebUIPort,
			"ws_port":    cfg.Services.Astrbot.WSPort,
			"pid":        process.GetPidOnPort(cfg.Services.Astrbot.WebUIPort),
		},
		"wechat": map[string]any{
			"running":   process.PortListening(cfg.Services.Wechat.APIPort),
			"api_port":  cfg.Services.Wechat.APIPort,
			"pid":       process.GetPidOnPort(cfg.Services.Wechat.APIPort),
		},
		"qr": map[string]any{
			"running": process.PortListening(cfg.Services.Qr.Port),
			"port":    cfg.Services.Qr.Port,
			"pid":     process.GetPidOnPort(cfg.Services.Qr.Port),
		},
	}
}

// which 返回可执行文件路径 (PATH 查找)
func which(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}