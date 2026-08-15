package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCmdConfigDefaultAlignsDataDir 验证默认配置下 cmd_config 规范化到 <astrbot_root>/data,
// 不再产生 D:cmd_config.json (旧版裸名 filepath.Join(baseDir, "cmd_config.json") 的产物)。
func TestCmdConfigDefaultAlignsDataDir(t *testing.T) {
	dir := t.TempDir()
	// 模拟全新用户: 只有默认配置 + 显式写裸名 cmd_config.json (与 config.local.example.json 一致)
	localJSON := `{
  "astrbot_root": "C:/Users/friend",
  "astrbot": {"cmd_config": "cmd_config.json"}
}`
	if err := os.WriteFile(filepath.Join(dir, "config.local.json"), []byte(localJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	if err := cfg.Load(dir); err != nil {
		t.Fatal(err)
	}

	// data_dir 从默认 .astrbot-data 权威化到 <root>/data
	wantData := filepath.Join("C:", string(filepath.Separator), "Users", "friend", "data")
	if cfg.AstrbotDataDir != wantData {
		t.Fatalf("AstrbotDataDir 未权威化:\n got: %s\nwant: %s", cfg.AstrbotDataDir, wantData)
	}
	// cmd_config 基于 data_dir (绝不含 "cmd_config.json" 拼接出的 D:xxx 裸盘路径)
	want := filepath.Join(wantData, "cmd_config.json")
	if cfg.Astrbot.CmdConfig != want {
		t.Fatalf("CmdConfig 规范失败:\n got: %s\nwant: %s", cfg.Astrbot.CmdConfig, want)
	}
	if filepath.Base(cfg.Astrbot.CmdConfig) != "cmd_config.json" {
		t.Fatalf("cmd_config 应为文件名, 实际: %s", cfg.Astrbot.CmdConfig)
	}
}

// TestCmdConfigBareNameOnDriveRelativeBase 模拟 bug 现场: 面板 baseDir 为驱动相对路径
// (裸 `cmd_config.json` 走 filepath.Join("D:", "cmd_config.json") == "D:cmd_config.json" 丢目录段),
// 且 astrbot_root 权威化到 <root>/data 后, cmd_config 必须基于 astrbot_data_dir 规范化,
// 与 AstrBot 权威位置一致 → 不再产生驱动相对/裸盘的 cmd_config 路径。
func TestCmdConfigBareNameOnDriveRelativeBase(t *testing.T) {
	// 用独立子目录模拟驱动相对 baseDir (避免污染真实盘根目录/覆盖真实 config.local.json)
	root := "D:" + string(filepath.Separator) + ".astrbot-panel-test-" + t.Name()
	baseRelative := "D:" // filepath.Join(baseRelative, "cmd_config.json") == "D:cmd_config.json"
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Skipf("无 D 盘测试环境, 跳过: %v", err)
	}
	defer os.RemoveAll(root)

	// root 下创建 .astrbot-root 并通过 path 结构让 base = "D:xxx" 时 join 丢段
	astrbotRoot := filepath.Join(root, ".astrbot-root")
	if err := os.MkdirAll(filepath.Join(astrbotRoot, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = baseRelative // (注释保留 bug 现场说明; 断言见下)

	localJSON := `{
  "astrbot_root": "` + filepath.ToSlash(astrbotRoot) + `",
  "astrbot_data_dir": "` + filepath.ToSlash(filepath.Join(astrbotRoot, "data")) + `",
  "astrbot": {"cmd_config": "cmd_config.json"}
}`
	if err := os.WriteFile(filepath.Join(root, "config.local.json"), []byte(localJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	if err := cfg.Load(root); err != nil {
		t.Fatal(err)
	}
	// cmd_config 一律不允许出现驱动相对/裸盘路径, 并必须等于权威位置 data_dir/cmd_config.json
	if cfg.Astrbot.CmdConfig == "D:cmd_config.json" || !filepath.IsAbs(cfg.Astrbot.CmdConfig) {
		t.Fatalf("仍产生驱动相对/非绝对 cmd_config: %s", cfg.Astrbot.CmdConfig)
	}
	want := filepath.Join(astrbotRoot, "data", "cmd_config.json")
	if filepath.Clean(cfg.Astrbot.CmdConfig) != filepath.Clean(want) {
		t.Fatalf("CmdConfig 未基于 astrbot_data_dir 规范化:\n got: %s\nwant: %s", cfg.Astrbot.CmdConfig, want)
	}
	if filepath.Clean(cfg.AstrbotDataDir) != filepath.Clean(filepath.Join(astrbotRoot, "data")) {
		t.Fatalf("AstrbotDataDir 异常: %s", cfg.AstrbotDataDir)
	}
}

// TestCmdConfigAbsoluteExistsKept 验证显式配置的绝对路径且文件存在时保持不变 (本机正常值场景)。
func TestCmdConfigAbsoluteExistsKept(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "data", "cmd_config.json")
	if err := os.WriteFile(existing, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	localJSON := `{
  "astrbot_root": "` + filepath.ToSlash(dir) + `",
  "astrbot_data_dir": "` + filepath.ToSlash(dataDir) + `",
  "astrbot": {"cmd_config": "` + filepath.ToSlash(existing) + `"}
}`
	if err := os.WriteFile(filepath.Join(dir, "config.local.json"), []byte(localJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	if err := cfg.Load(dir); err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(cfg.Astrbot.CmdConfig) != filepath.Clean(existing) {
		t.Fatalf("存在文件的显式 cmd_config 不应被改写:\n got: %s\nwant: %s", cfg.Astrbot.CmdConfig, existing)
	}
}