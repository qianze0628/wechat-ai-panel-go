package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"wechat-ai-panel/internal/config"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// TestExtractCredsFirstLogin 验证: 首次部署 (password_change_required=true) 不算"已修改密码",
// 且能从 AstrBot 启动日志提取明文初始密码
func TestExtractCredsFirstLogin(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WechatBotDir = filepath.Join(root, "bot")
	cfg.AstrbotRoot = root
	cfg.AstrbotDataDir = filepath.Join(root, "data")
	cfg.Astrbot.CmdConfig = filepath.Join(root, "data", "cmd_config.json")
	cfg.Logs.AstrbotStdout = filepath.Join(root, "astrbot.log")
	_ = os.MkdirAll(filepath.Join(root, "data"), 0o755)
	// 模拟 AstrBot 首次部署的 cmd_config: 初始密码哈希 + change_required + upgraded
	m := map[string]any{
		"dashboard": map[string]any{
			"username": "astrbot",
			"password": "hashedmd5",
			"password_storage_upgraded": true,
			"password_change_required":  true,
		},
	}
	if err := utilWriteJSON(cfg.Astrbot.CmdConfig, m); err != nil {
		t.Fatal(err)
	}
	// AstrBot 启动日志里有初始密码
	_ = os.WriteFile(cfg.Logs.AstrbotStdout, []byte("[12:00:00] [INFO] \r\n  ➜  Initial username: astrbot\r\n  ➜  Initial password: Aa12345678!\r\n  ➜  Change it after logging in\r\n"), 0o644)
	creds := ExtractCreds(&cfg)
	if creds["first_login"] != true {
		t.Errorf("首次登录应 first_login=true, got %v", creds["first_login"])
	}
	if creds["password_changed"] != false {
		t.Errorf("首次登录不应 password_changed=true, got %v", creds["password_changed"])
	}
	if creds["password"] != "Aa12345678!" {
		t.Errorf("应从日志提取初始密码, got %v", creds["password"])
	}
}

// TestExtractCredsChanged 验证: 用户已改密 (change_required=false) → password_changed=true
func TestExtractCredsChanged(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.AstrbotRoot = root
	cfg.AstrbotDataDir = filepath.Join(root, "data")
	cfg.Astrbot.CmdConfig = filepath.Join(root, "data", "cmd_config.json")
	_ = os.MkdirAll(filepath.Join(root, "data"), 0o755)
	m := map[string]any{
		"dashboard": map[string]any{
			"username": "astrbot",
			"password_storage_upgraded": true,
			"password_change_required":  false,
		},
	}
	if err := utilWriteJSON(cfg.Astrbot.CmdConfig, m); err != nil {
		t.Fatal(err)
	}
	creds := ExtractCreds(&cfg)
	if creds["password_changed"] != true {
		t.Errorf("已改密应 password_changed=true, got %v", creds["password_changed"])
	}
	if creds["first_login"] != false {
		t.Errorf("已改密应 first_login=false, got %v", creds["first_login"])
	}
}

// TestExtractInitialPasswordFromLog 验证日志解析 (含 CRLF + ANSI 色码)
func TestExtractInitialPasswordFromLog(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Logs.AstrbotStdout = filepath.Join(root, "astrbot.log")
	// 带 ANSI 色码的真实日志片段
	log := "\x1b[32m[INFO]\x1b[0m \r\n  \u279c  Initial username: astrbot\r\n  \u279c  Initial password: Zx9$kLm2@wQ7\r\n\x1b[0m"
	_ = os.WriteFile(cfg.Logs.AstrbotStdout, []byte(log), 0o644)
	got := extractInitialPasswordFromLog(&cfg)
	if got != "Zx9$kLm2@wQ7" {
		t.Errorf("密码提取失败: %q", got)
	}
}

// utilWriteJSON 测试辅助: 写 JSON 文件
func utilWriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := jsonMarshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
