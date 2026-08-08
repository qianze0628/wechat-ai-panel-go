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
	// 自启启动方式: 记录日志 (便于排查"开机是否拉起")
	if api.HandleAutostartArg() {
		api.LogAutostart("autostart start at " + time.Now().Format("2006-01-02 15:04:05"))
	}
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
			"version": api.VersionTag(),
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
	// 插件中心 (扫描 AstrBot 插件 + 配置读写 + 连通状态)
	srv.RegisterPluginCenter(&cfg)
	// AstrBot 配置文件读写 (仿 AstrBot 配置文件页, 自动备份)
	srv.RegisterCmdConfig(&cfg)
	// 二维码
	srv.RegisterQr(&cfg)
	// 安装引擎
	srv.RegisterInstall(&cfg)
	api.SetInstallLogPath(filepath.Join(baseDir, "logs", "install.log"))
	// 国内镜像源 (npm/pypi/git 加速, 免代理)
	api.SetMirrors(cfg.Mirrors.NpmRegistry, cfg.Mirrors.PypiIndex, cfg.Mirrors.GitCloneProxy)
	// 面板认证
	srv.RegisterAuth(&cfg)
	// 面板设置 (认证/备份开关), 需在 RegisterAuth 之后
	srv.RegisterSettings(&cfg)
	api.SetSettingsConfigPath(filepath.Join(baseDir, "config.json"))
	api.SetBackupEnabled(cfg.BackupEnabled)
	// AstrBot 群聊 ICL 补丁自动恢复 (升级冲掉后自动重打, 防止群聊"答非所问")
	api.EnsureGroupChatPatch()
	srv.RegisterPatch()
	// 开机自启 (Windows 注册表 Run 键), 自启日志在 logs/autostart.log
	api.SetAutostartLogDir(filepath.Join(baseDir, "logs"))
	srv.RegisterAutostart()
	// 更新检测 (GitHub latest + IP 判断国内镜像)
	srv.RegisterUpdate()
	// 面板内置自动更新 (下载→替换→重启)
	api.SetVersionTag("v0.2.1")
	srv.RegisterUpdateApply()
	// 服务守护: 启动自动拉起 + 每 30s 健康检查掉线自动恢复
	// (电脑重启后打开面板即全链路恢复, 无需手动逐个启动)
	svc.EnsureAll()
	go svc.Supervise(30 * time.Second)
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
// 注意: 必须优先 uv tools 版。本机若存在 D:/python 全局旧副本 (4.27.1 等),
// 版本落后且无群聊补丁, fallback 到它会"AI 变蠢"。因此 fallback 时打日志警告。
func findAstrbotExe() string {
	candidates := []string{
		filepath.Join(os.Getenv("USERPROFILE"), `AppData\Roaming\uv\tools\astrbot\Scripts\astrbot.exe`),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	p := which("astrbot")
	if p != "" {
		log.Printf("[warn] 未找到 uv tools 版 astrbot, 使用 PATH 副本 %s (可能版本落后, 建议重装: uv tool install astrbot)", p)
	}
	return p
}