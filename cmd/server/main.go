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
	"time"

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
	// 向上查找包含 config.json / config.local.json 的目录作为配置根:
	// - exe 在 bin\ 时, 配置在项目根 → 应使用项目根
	// - 开发模式 (go run) 时, 源码目录有 config → 直接命中
	// - 都找不到则回退 cwd (双击 exe 时 cwd 是 exe 目录, 此时用默认配置)
	if !configDirHasConfig(baseDir) {
		if cwd, e := os.Getwd(); e == nil && configDirHasConfig(cwd) {
			baseDir = cwd
		} else {
			// 逐级向上找 (最多 5 层, 避免无限)
			for i := 0; i < 5; i++ {
				parent := filepath.Dir(baseDir)
				if parent == baseDir {
					break
				}
				baseDir = parent
				if configDirHasConfig(baseDir) {
					break
				}
			}
		}
	}
	log.Printf("[panel] 配置目录: %s", baseDir)

	cfg := config.Default()
	if err := cfg.Load(baseDir); err != nil {
		log.Printf("配置校验告警: %v", err)
	}

	// 聚合 /api/status 状态
	statusFn := func() any {
		astrbotExe := findAstrbotExe()
		wechatPkg := filepath.Join(cfg.WechatBotDir, "package.json")
		_, wechatPkgErr := os.Stat(wechatPkg)
		_, wechatNMErr := os.Stat(filepath.Join(cfg.WechatBotDir, "node_modules"))
		_, cmdCfgErr := os.Stat(cfg.Astrbot.CmdConfig)
		envMap := map[string]any{
			"node":   map[string]any{"installed": which("node") != "", "path": which("node")},
			"npm":    map[string]any{"installed": which("npm") != "", "path": which("npm")},
			"uv":     map[string]any{"installed": which("uv") != "", "path": which("uv")},
			"python": map[string]any{"installed": which("python") != "", "path": which("python")},
			"astrbot": map[string]any{"installed": astrbotExe != "", "path": astrbotExe},
			"wechat_bot": map[string]any{
				"installed": wechatPkgErr == nil, "deps_ready": wechatNMErr == nil,
				"path": cfg.WechatBotDir,
			},
			"astrbot_root": map[string]any{"ok": true, "path": cfg.AstrbotRoot},
			"cmd_config":   map[string]any{"exists": cmdCfgErr == nil, "path": cfg.Astrbot.CmdConfig},
		}
		ce := cfg.ConfigErrors
		if ce == nil {
			ce = []string{}
		}
		return map[string]any{
			"ok":      true,
			"version": "go-v0.2",
			"env":     envMap,
			"services":           serviceStatus(&cfg),
			"creds":              api.ExtractCreds(&cfg),
			"astrbot_configured": api.AstrbotConfigured(&cfg),
			"config_errors":      ce,
			"config": map[string]any{
				"wechat_bot_dir":  cfg.WechatBotDir,
				"astrbot_root":    cfg.AstrbotRoot,
				"astrbot_data_dir": cfg.AstrbotDataDir,
				"cmd_config":      cfg.Astrbot.CmdConfig,
				"cmd_config_mtime": nil,
				"port":            cfg.Port,
			},
		}
	}

	srv := api.New(webSub)
	srv.SetStatusHandler(statusFn)
	srv.SetAstrbotWebUIPort(cfg.Services.Astrbot.WebUIPort)
	// 服务控制器
	svc := process.NewServices(&cfg)
	srv.SetServiceController(svc)
	// 监控类 API (env/system/logs/messages)
	srv.RegisterMonitor(&cfg)
	// 备份目录 (与旧面板共享)
	if cfg.BackupDir != "" {
		api.SetBackupDir(cfg.BackupDir)
	}
	// AstrBot 集成 (creds/whitelist/setup/backups)
	srv.RegisterAstrbot(&cfg)
	// 二维码
	srv.RegisterQr(&cfg)
	// 安装引擎
	srv.RegisterInstall(&cfg)
	// 面板认证
	srv.RegisterAuth(&cfg)
	// 面板设置 (认证/备份开关), 需在 RegisterAuth 之后
	srv.RegisterSettings(&cfg)
	api.SetSettingsConfigPath(filepath.Join(baseDir, "config.json"))
	api.SetBackupEnabled(cfg.BackupEnabled)
	// 重启 AstrBot 注入 (恢复/配置用)
	api.SetRestartFn(func(c *config.Config) {
		svc.Stop("astrbot")
		time.Sleep(time.Second)
		svc.Start("astrbot")
	})

	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", host, cfg.Port)
	log.Printf("[panel] 微信 AI 管理面板 (Go): http://%s:%d", host, cfg.Port)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatalf("监听失败: %v", err)
	}
}

// configDirHasConfig 判断目录是否包含面板配置文件 (config.json 或 config.local.json)
func configDirHasConfig(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "config.local.json")); err == nil {
		return true
	}
	return false
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

// findAstrbotExe 查找 astrbot 可执行 (uv tools → PATH)
func findAstrbotExe() string {
	candidates := []string{
		filepath.Join(os.Getenv("USERPROFILE"), `AppData\Roaming\uv\tools\astrbot\Scripts\astrbot.exe`),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return which("astrbot")
}