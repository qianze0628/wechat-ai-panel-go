// Package api OneBot 配置 / 备份恢复 (setup_astrbot_platform 等价)
package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/util"
)

// panelBaseDir 程序基础目录 (通过 env 注入; 兜底 cwd)
var panelBaseDir = func() string {
	cwd, _ := os.Getwd()
	return cwd
}

// backupDirVar 备份目录 (由 SetBackupDir 注入, 默认 Go 项目 runtime)
var backupDirVar = ""

// SetBackupDir 设置备份目录 (应与旧面板共享)
func SetBackupDir(dir string) { backupDirVar = dir }

// backupDir 备份目录
func backupDir() string {
	if backupDirVar != "" {
		return backupDirVar
	}
	return filepath.Join(panelBaseDir(), "runtime", "backups")
}

// backupRawFile 备份原始文件到 runtime/backups/时间戳/
func backupRawFile(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	stamp := time.Now().Format("20060102-150405")
	dir := filepath.Join(backupDir(), stamp)
	os.MkdirAll(dir, 0o755)
	dest := filepath.Join(dir, filepath.Base(path))
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	os.WriteFile(dest, data, 0o644)
	return dest
}

// platformEntry 构造 aiocqhttp 平台配置
func platformEntry(cfg *config.Config) map[string]any {
	return map[string]any{
		"id":                 cfg.Astrbot.PlatformID,
		"type":               cfg.Astrbot.PlatformTyp,
		"enable":             true,
		"ws_reverse_host":    cfg.Astrbot.WSHost,
		"ws_reverse_port":    cfg.Astrbot.WSPort,
		"ws_reverse_token":   cfg.Astrbot.WSToken,
	}
}

// setupOneBot 应用 OneBot 配置 (备份 + 原子写) + 重启 AstrBot
func setupOneBot(cfg *config.Config) (string, error) {
	cfgPath := cfg.Astrbot.CmdConfig
	if _, err := os.Stat(cfgPath); err != nil {
		return "", errors.New("cmd_config.json 不存在: " + cfgPath)
	}
	// 备份原始
	backupRawFile(cfgPath)
	m, err := util.ReadJSONFile(cfgPath)
	if err != nil {
		return "", fmt.Errorf("解析失败: %w", err)
	}
	// platform 数组
	platforms, _ := m["platform"].([]any)
	if platforms == nil {
		platforms = []any{}
	}
	entry := platformEntry(cfg)
	found := false
	newPlatforms := make([]any, 0, len(platforms)+1)
	for _, p := range platforms {
		pm, _ := p.(map[string]any)
		if pm != nil && pm["id"] == cfg.Astrbot.PlatformID {
			newPlatforms = append(newPlatforms, entry)
			found = true
		} else {
			newPlatforms = append(newPlatforms, p)
		}
	}
	if !found {
		newPlatforms = append(newPlatforms, entry)
	}
	m["platform"] = newPlatforms
	// wake_prefix
	m["wake_prefix"] = cfg.Astrbot.WakePrefix
	// platform_settings: 私聊免前缀
	ps, _ := m["platform_settings"].(map[string]any)
	if ps == nil {
		ps = map[string]any{}
	}
	ps["friend_message_needs_wake_prefix"] = false
	m["platform_settings"] = ps
	// dashboard
	dash, _ := m["dashboard"].(map[string]any)
	if dash != nil {
		dash["port"] = cfg.Astrbot.Dashboard.Port
		dash["host"] = cfg.Astrbot.Dashboard.Host
		m["dashboard"] = dash
	}
	// 原子写 (BOM)
	if err := util.AtomicWriteJSON(cfgPath, m, true); err != nil {
		return "", fmt.Errorf("写入失败: %w", err)
	}
	return "OneBot 配置已更新", nil
}

// setupPreview 配置变更预览 (不写入)
func setupPreview(cfg *config.Config) map[string]any {
	cfgPath := cfg.Astrbot.CmdConfig
	if _, err := os.Stat(cfgPath); err != nil {
		return map[string]any{"ok": false, "message": "cmd_config.json 不存在"}
	}
	m, err := util.ReadJSONFile(cfgPath)
	if err != nil {
		return map[string]any{"ok": false, "message": "解析失败: " + err.Error()}
	}
	var changes []string
	platforms, _ := m["platform"].([]any)
	if platforms == nil {
		changes = append(changes, "platform 数组不存在, 将创建")
	} else {
		found := false
		for _, p := range platforms {
			pm, _ := p.(map[string]any)
			if pm != nil && pm["id"] == cfg.Astrbot.PlatformID {
				found = true
				break
			}
		}
		if !found {
			changes = append(changes, "将新增平台 "+cfg.Astrbot.PlatformID)
		}
	}
	if len(changes) == 0 {
		changes = append(changes, "无变更")
	}
	return map[string]any{
		"ok": true, "changes": changes, "untouched": []string{"模型 provider (不改动)"},
		"need_restart": true, "cmd_config": cfgPath, "backup_dir": backupDir(),
	}
}

// restoreConfig 从备份恢复
func restoreConfig(cfg *config.Config, path string) error {
	if path == "" {
		return errors.New("备份文件不存在")
	}
	cfgPath := cfg.Astrbot.CmdConfig
	backupRawFile(cfgPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// 校验备份可解析
	if _, err := util.ReadJSONFile(path); err != nil {
		return fmt.Errorf("备份不是有效 JSON: %w", err)
	}
	// 写回 (临时文件 + rename 原子)
	tmp := cfgPath + ".tmp-restore"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	os.Remove(cfgPath)
	if err := os.Rename(tmp, cfgPath); err != nil {
		return err
	}
	// 重启 AstrBot
	restartFn(cfg)
	return nil
}

// restartFn 由 main 注入 (避免循环依赖)
var restartFn = func(cfg *config.Config) {
	// 默认空实现; main 里用 process 设置
}

// SetRestartFn 注入 AstrBot 重启函数
func SetRestartFn(fn func(cfg *config.Config)) { restartFn = fn }