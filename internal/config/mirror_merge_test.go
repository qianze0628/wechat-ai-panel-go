package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMirrorDefaultsFromConfigLocal 验证: config.local.json 无 mirrors key 时,
// deepMerge 保留 Default() 中的镜像默认值 (npmmirror / aliyun / 空 git proxy)
func TestMirrorDefaultsFromConfigLocal(t *testing.T) {
	dir := t.TempDir()
	// 模拟 main.go m68 的默认构造路径: Default() 起点 + config.json + config.local.json 均无 mirrors
	cfg := Default()

	// 写入两个无 mirrors 的配置文件 (复制用户真实 config.local.json 场景)
	cfgJSON := `{"backup_enabled": true, "panel_password": ""}`
	localJSON := `{
  "port": 8081,
  "wechat_bot_dir": "C:/x/wechat-bot-windows",
  "astrbot_root": "C:/Users/x",
  "astrbot_data_dir": "C:/Users/x/data",
  "astrbot": {"cmd_config": "C:/Users/x/cmd_config.json"}
}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.local.json"), []byte(localJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg.Load(dir)

	got := fmt.Sprintf("%s | %s | %s", cfg.Mirrors.NpmRegistry, cfg.Mirrors.PypiIndex, cfg.Mirrors.GitCloneProxy)
	want := "https://registry.npmmirror.com | https://mirrors.aliyun.com/pypi/simple/ | "
	if got != want {
		t.Fatalf("mirrors 未回退默认值:\n got: %s\nwant: %s", got, want)
	}
	t.Logf("OK: mirrors = [%s]", got)
}