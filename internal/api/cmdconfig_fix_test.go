package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/util"
)

// TestEffectiveCmdConfigFallback 验证: 配置路径不存在时回退到 <root>/data/cmd_config.json
func TestEffectiveCmdConfigFallback(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.AstrbotRoot = root
	// 配置路径不存在 (典型: 旧配置解析到 D:\cmd_config.json)
	cfg.Astrbot.CmdConfig = filepath.Join(tmp, "cmd_config.json")
	got := cfg.EffectiveCmdConfig()
	want := filepath.Join(root, "data", "cmd_config.json")
	if got != want {
		t.Errorf("EffectiveCmdConfig = %s, want %s", got, want)
	}
}

// TestEnsureCmdConfigExists 验证: 一键配置时自动生成最小 cmd_config.json
func TestEnsureCmdConfigExists(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.AstrbotRoot = root
	cfg.Astrbot.CmdConfig = filepath.Join(root, "data", "cmd_config.json")
	path, err := ensureCmdConfigExists(&cfg)
	if err != nil {
		t.Fatalf("ensureCmdConfigExists: %v", err)
	}
	if path != cfg.Astrbot.CmdConfig {
		t.Errorf("path = %s", path)
	}
	// 生成的文件应可解析为 JSON
	m, err := util.ReadJSONFile(path)
	if err != nil {
		t.Fatalf("生成文件不可解析: %v", err)
	}
	if m["config_version"] == nil {
		t.Error("缺少 config_version")
	}
	if m["platform"] == nil {
		t.Error("缺少 platform 数组")
	}
}

// TestLoadCmdConfigFallback 验证: config.Load 后 CmdConfig 自动回退权威路径
func TestLoadCmdConfigFallback(t *testing.T) {
	tmp := t.TempDir()
	// 构造一个带不存在 cmd_config 的 config 文件
	cfgJSON := `{"astrbot_root": "{{ROOT}}", "astrbot": {"cmd_config": "cmd_config.json"}}`
	root := filepath.Join(tmp, "astrbot-root")
	// JSON 内路径用正斜杠 (反斜杠需转义, 简化测试)
	cfgJSON = strings.ReplaceAll(cfgJSON, "{{ROOT}}", strings.ReplaceAll(root, "\\", "/"))
	if err := os.WriteFile(filepath.Join(tmp, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := cfg.Load(tmp); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "data", "cmd_config.json")
	if cfg.Astrbot.CmdConfig != want {
		t.Errorf("Load 后 CmdConfig = %s, want %s", cfg.Astrbot.CmdConfig, want)
	}
}
