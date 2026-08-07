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
	PanelPassword  string     `json:"panel_password"`
	ProjectRoot    string     `json:"project_root"`
	WechatBotDir   string     `json:"wechat_bot_dir"`
	AstrbotRoot    string     `json:"astrbot_root"`
	AstrbotDataDir string     `json:"astrbot_data_dir"`
	QrServerScript string     `json:"qr_server_script"`
	WechatBotServe string     `json:"wechat_bot_serve"`
	BackupDir      string     `json:"backup_dir"`
	Logs           LogPaths   `json:"logs"`
	Services       Services   `json:"services"`
	Astrbot        AstrConfig `json:"astrbot"`
	ConfigErrors   []string   `json:"-"`
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

// Default 默认配置 (与 Python DEFAULT_CONFIG 一致)
func Default() Config {
	return Config{
		Port:            8080,
		ProjectRoot:     "C:/Users/YMB/Desktop/wechat",
		WechatBotDir:    "C:/Users/YMB/Desktop/wechat/wechat-bot-windows",
		AstrbotRoot:     "C:/Users/YMB",
		AstrbotDataDir:  "C:/Users/YMB/data",
		QrServerScript:  "C:/Users/YMB/Desktop/wechat/qr-server.js",
		WechatBotServe:  "ChatGPT",
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
			CmdConfig:   "C:/Users/YMB/data/cmd_config.json",
			PlatformID:  "wechat-bridge",
			PlatformTyp: "aiocqhttp",
			WSHost:      "127.0.0.1",
			WSPort:      20129,
			WakePrefix:  []string{"/"},
			Dashboard:   Dashboard{Enable: true, Host: "0.0.0.0", Port: 6185},
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
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
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

	return c.Validate()
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
	c.ConfigErrors = errs
	return nil // 错误仅记录, 不阻断
}