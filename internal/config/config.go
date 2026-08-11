// Package config 加载面板配置 (兼容 Python 版 config.json + config.local.json)
// 支持: UTF-8 BOM、递归深度合并、相对路径解析、校验错误收集
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config 面板配置结构 (对应 config.json 顶层)
type Config struct {
	Port           int        `json:"port"`
	Host           string     `json:"host"` // 监听地址 (默认 127.0.0.1; Docker/远程部署设 0.0.0.0)
	PanelPassword  string     `json:"panel_password"`
	ProjectRoot    string     `json:"project_root"`
	WechatBotDir   string     `json:"wechat_bot_dir"`
	AstrbotRoot    string     `json:"astrbot_root"`
	AstrbotDataDir string     `json:"astrbot_data_dir"`
	QrServerScript string     `json:"qr_server_script"`
	WechatBotServe string     `json:"wechat_bot_serve"`
	BackupDir      string     `json:"backup_dir"`
	BackupEnabled  bool       `json:"backup_enabled"`  // 是否创建配置备份 (默认 true)
	WechatBotRepo  string     `json:"wechat_bot_repo"` // wechat-bot 优化版源码仓库 (非空时缺源码自动 clone)
	Mirrors        Mirrors    `json:"mirrors"` // 国内镜像源 (加速下载, 免代理)
	Logs           LogPaths   `json:"logs"`
	Services       Services   `json:"services"`
	Astrbot        AstrConfig `json:"astrbot"`
	ConfigErrors   []string   `json:"-"`
}

// Mirrors 国内镜像源配置 (默认国内常用镜像, 可按需覆盖/留空禁用)
type Mirrors struct {
	NpmRegistry   string `json:"npm_registry"`   // npm 镜像 (默认 npmmirror)
	PypiIndex     string `json:"pypi_index"`     // pip/uv 镜像 (默认阿里云)
	GitCloneProxy string `json:"git_clone_proxy"` // git clone 加速前缀 (如 ghproxy 类, 空=直连)
}

// LogPaths 日志路径
type LogPaths struct {
	AstrbotStdout     string `json:"astrbot_stdout"`
	AstrbotStderr     string `json:"astrbot_stderr"`
	WechatStdout      string `json:"wechat_stdout"`
	WechatStderr      string `json:"wechat_stderr"`
	QrStdout          string `json:"qr_stdout"`
	QrStderr          string `json:"qr_stderr"`
	AstrbotCaptureLog string `json:"astrbot_capture_log"`
	WechatCaptureLog  string `json:"wechat_capture_log"`
}

// Services 各服务端口
type Services struct {
	Astrbot AstrbotService `json:"astrbot"`
	Wechat  WechatService  `json:"wechat"`
	Qr      QrService      `json:"qr"`
}

type AstrbotService struct {
	WebUIPort int `json:"webui_port"`
	WSPort    int `json:"ws_port"`
}

type WechatService struct {
	APIPort int `json:"api_port"`
}

type QrService struct {
	Port int `json:"port"`
}

// AstrConfig AstrBot 相关配置
type AstrConfig struct {
	CmdConfig   string    `json:"cmd_config"`
	PlatformID  string    `json:"platform_id"`
	PlatformTyp string    `json:"platform_type"`
	WSHost      string    `json:"ws_host"`
	WSPort      int       `json:"ws_port"`
	WSToken     string    `json:"ws_token"`
	WakePrefix  []string  `json:"wake_prefix"`
	Dashboard   Dashboard `json:"dashboard"`
}

// Dashboard AstrBot dashboard
type Dashboard struct {
	Enable bool   `json:"enable"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
}

// Default 默认配置 (与 Python DEFAULT_CONFIG 一致; 路径用相对值, 在 Load 里基于程序目录解析,
// 以便跨平台 (Windows/Linux/macOS) 与 Docker 部署开箱即用)
func Default() Config {
	return Config{
		Port:            8080,
		Host:            "127.0.0.1",
		ProjectRoot:     "runtime",
		WechatBotDir:    "wechat-bot-windows",
		AstrbotRoot:     ".astrbot-root",
		AstrbotDataDir:  ".astrbot-data",
		QrServerScript:  "qr-server.js",
		WechatBotServe:  "ChatGPT",
		// wechat-bot 优化版源码仓库 (默认值; 全新用户没配 config 时也能自动 clone)
		WechatBotRepo:  "https://github.com/qianze0628/wechat-bot-optimized.git",
		BackupEnabled:  true, // 默认备份开启
		Logs: LogPaths{
			AstrbotStdout: "logs/astrbot_boot.log",
			AstrbotStderr: "logs/astrbot_boot_err.log",
			WechatStdout:  "logs/bot_boot.log",
			WechatStderr:  "logs/bot_boot_err.log",
			QrStdout:      "logs/qr_boot.log",
			QrStderr:      "logs/qr_boot_err.log",
		},
		Services: Services{
			Astrbot: AstrbotService{WebUIPort: 6185, WSPort: 20129},
			Wechat:  WechatService{APIPort: 6189},
			Qr:      QrService{Port: 8090},
		},
		Astrbot: AstrConfig{
			CmdConfig:   "cmd_config.json",
			PlatformID:  "wechat-bridge",
			PlatformTyp: "aiocqhttp",
			WSHost:      "127.0.0.1",
			WSPort:      20129,
			WakePrefix:  []string{"/"},
			Dashboard:   Dashboard{Enable: true, Host: "0.0.0.0", Port: 6185},
		},
		// 国内镜像源默认值 (免代理加速下载; 用户可覆盖/清空禁用)
		// 注: git_clone_proxy 默认空 (ghproxy 类公共服务不稳定, 失败率高;
		// 用户在 config 里可配置一个可用的加速前缀)
		Mirrors: Mirrors{
			NpmRegistry:   "https://registry.npmmirror.com",
			PypiIndex:     "https://mirrors.aliyun.com/pypi/simple/",
			GitCloneProxy: "",
		},
	}
}

// readJSONFile 读 JSON 文件 (兼容 UTF-8 BOM)
func readJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = []byte(strings.TrimPrefix(string(data), "\ufeff"))
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// deepMerge 递归深度合并: override 覆盖 base (dict 递归, 其余覆盖)
func deepMerge(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		if bval, ok := out[k]; ok {
			if bm, ok1 := bval.(map[string]any); ok1 {
				if om, ok2 := v.(map[string]any); ok2 {
					out[k] = deepMerge(bm, om)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}

// structToMap 结构体 → map (深合并的起点)
func structToMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// Load 加载配置: baseDir 为程序目录
func (c *Config) Load(baseDir string) error {
	defaults := Default()
	merged := structToMap(&defaults)

	// config.json
	cfgPath := filepath.Join(baseDir, "config.json")
	if _, err := os.Stat(cfgPath); err == nil {
		user, err := readJSONFile(cfgPath)
		if err != nil {
			fmt.Printf("[panel] config.json 读取失败(使用默认): %v\n", err)
		} else {
			merged = deepMerge(merged, user)
		}
	}
	// config.local.json (本地覆盖)
	localPath := filepath.Join(baseDir, "config.local.json")
	if _, err := os.Stat(localPath); err == nil {
		local, err := readJSONFile(localPath)
		if err != nil {
			fmt.Printf("[panel] config.local.json 读取失败(忽略): %v\n", err)
		} else {
			merged = deepMerge(merged, local)
			fmt.Println("[panel] 已加载 config.local.json (本地覆盖)")
		}
	}

	raw, _ := json.Marshal(merged)
	if err := json.Unmarshal(raw, c); err != nil {
		return fmt.Errorf("配置解析失败: %w", err)
	}

	// 相对路径基于 baseDir 解析
	// Unix 下 "/xxx" 会被 IsAbs 判为绝对路径; 若该路径不存在 (典型如配置里写了 /wechat-bot-windows
	// 或 /logs 但实际是相对意图), 回退到 baseDir 下同名路径, 避免指向根目录报错。
	resolve := func(p string) string {
		if p == "" {
			return p
		}
		if filepath.IsAbs(p) {
			if _, err := os.Stat(p); err == nil {
				return p
			}
			// 绝对路径不存在: 若看起来像相对路径误写 (去掉前导 / 后 baseDir 下存在) → 回退
			rel := strings.TrimLeft(p, "/\\")
			if rel != "" {
				cand := filepath.Join(baseDir, rel)
				if _, err := os.Stat(cand); err == nil {
					return cand
				}
			}
			// 仍不存在: 保持原样 (由后续逻辑决定; 至少不崩溃)
			return p
		}
		return filepath.Join(baseDir, p)
	}
	c.ProjectRoot = resolve(c.ProjectRoot)
	c.WechatBotDir = resolve(c.WechatBotDir)
	c.AstrbotRoot = resolve(c.AstrbotRoot)
	c.AstrbotDataDir = resolve(c.AstrbotDataDir)
	c.QrServerScript = resolve(c.QrServerScript)
	c.Astrbot.CmdConfig = resolve(c.Astrbot.CmdConfig)
	c.Logs.AstrbotStdout = resolve(c.Logs.AstrbotStdout)
	c.Logs.AstrbotStderr = resolve(c.Logs.AstrbotStderr)
	c.Logs.WechatStdout = resolve(c.Logs.WechatStdout)
	c.Logs.WechatStderr = resolve(c.Logs.WechatStderr)
	c.Logs.QrStdout = resolve(c.Logs.QrStdout)
	c.Logs.QrStderr = resolve(c.Logs.QrStderr)
	c.Logs.AstrbotCaptureLog = resolve(c.Logs.AstrbotCaptureLog)
	c.Logs.WechatCaptureLog = resolve(c.Logs.WechatCaptureLog)

	// 日志目录不可写 (如容器里被解析到 /logs 根目录) 时, 回退到 baseDir/logs
	c.sanitizeLogPaths(baseDir)

	return c.Validate()
}

// sanitizeLogPaths 确保日志目录可写; 不可写时回退到 baseDir/logs
// (Linux 容器里 "logs/..." 若被误判为 /logs 或目录无权限, 会导致启动失败)
func (c *Config) sanitizeLogPaths(baseDir string) {
	fallback := filepath.Join(baseDir, "logs")
	_ = os.MkdirAll(fallback, 0o755)
	check := func(p *string) {
		if *p == "" {
			*p = filepath.Join(fallback, "panel.log")
			return
		}
		dir := filepath.Dir(*p)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			// 无法创建 → 回退
			*p = filepath.Join(fallback, filepath.Base(*p))
		}
	}
	check(&c.Logs.AstrbotStdout)
	check(&c.Logs.AstrbotStderr)
	check(&c.Logs.WechatStdout)
	check(&c.Logs.WechatStderr)
	check(&c.Logs.QrStdout)
	check(&c.Logs.QrStderr)
	check(&c.Logs.AstrbotCaptureLog)
	check(&c.Logs.WechatCaptureLog)
}

// Validate 校验配置 (错误记录到 ConfigErrors, 不阻断)
func (c *Config) Validate() error {
	var errs []string
	if c.Port <= 0 {
		errs = append(errs, "port 必须是正整数")
	}
	for _, check := range []struct{ name, val string }{
		{"project_root", c.ProjectRoot},
		{"wechat_bot_dir", c.WechatBotDir},
		{"astrbot_root", c.AstrbotRoot},
	} {
		if check.val == "" {
			errs = append(errs, check.name+" 不能为空")
		}
	}
	if c.Astrbot.CmdConfig == "" {
		errs = append(errs, "astrbot.cmd_config 不能为空")
	}
	// 修复 (2026-08-12): 配置指向不存在的盘符/目录 (如无 D 盘但写 D:\.astrbot-root) —
	// 明确提示用户, 避免实例信息"显示目录但实际不存在"的困惑 (前端顶部黄条提示)。
	if c.AstrbotRoot != "" {
		if _, err := os.Stat(c.AstrbotRoot); err != nil {
			// Windows 下可进一步判断盘符是否存在; 统一提示为目录不存在
			errs = append(errs, fmt.Sprintf("astrbot_root 目录不存在: %s (检查盘符/路径; 面板会自动回退到用户目录)", c.AstrbotRoot))
		}
	}
	if c.AstrbotDataDir != "" {
		if _, err := os.Stat(c.AstrbotDataDir); err != nil {
			errs = append(errs, fmt.Sprintf("astrbot_data_dir 目录不存在: %s (AstrBot 会回退到默认位置)", c.AstrbotDataDir))
		}
	}
	c.ConfigErrors = errs
	return nil // 错误仅记录, 不阻断
}