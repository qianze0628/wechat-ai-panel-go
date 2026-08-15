// Package api OneBot 配置 / 备份恢复 (setup_astrbot_platform 等价)
package api

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/util"
)

//go:embed assets/whitelist_manager/*
var assetsFS embed.FS

// panelBaseDir 程序基础目录 (通过 env 注入; 兜底 cwd)
var panelBaseDir = func() string {
	cwd, _ := os.Getwd()
	return cwd
}

// backupDirVar 备份目录 (由 SetBackupDir 注入, 默认 Go 项目 runtime)
var backupDirVar = ""

// backupEnabledVar 备份开关 (设置页可关, 默认 true)
var backupEnabledVar = true

// SetBackupDir 设置备份目录 (应与旧面板共享)
func SetBackupDir(dir string) { backupDirVar = dir }

// SetBackupEnabled 设置备份开关 (false 时跳过所有自动备份)
func SetBackupEnabled(enabled bool) { backupEnabledVar = enabled }

// backupDir 备份目录
func backupDir() string {
	if backupDirVar != "" {
		return backupDirVar
	}
	return filepath.Join(panelBaseDir(), "runtime", "backups")
}

// backupRawFile 备份原始文件到 runtime/backups/时间戳/
func backupRawFile(path string) string {
	if !backupEnabledVar {
		return "" // 备份开关已关闭
	}
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

// ensureCmdConfigExists 确保 cmd_config.json 存在: 不存在时自动生成最小合法配置。
// 第一性原理 (2026-08-15 v0.6.1): AstrBot 首次启动会自动补全缺失配置键
// (AstrBotConfig.check_config_integrity 递归补默认值), 所以面板只需生成最小骨架,
// 无需完整 DEFAULT_CONFIG。修复"一键配置 OneBot 报 cmd_config.json 不存在"死结。
func ensureCmdConfigExists(cfg *config.Config) (string, error) {
	cfgPath := cfg.Astrbot.CmdConfig
	if cfgPath == "" {
		cfgPath = cfg.EffectiveCmdConfig()
	}
	if _, err := os.Stat(cfgPath); err == nil {
		return cfgPath, nil // 已存在
	}
	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return "", fmt.Errorf("创建 cmd_config 目录失败: %v", err)
	}
	// 最小骨架 (AstrBot 启动时自动补全其余默认键)
	minimal := map[string]any{
		"config_version": 2,
		"platform":       []any{},
		"wake_prefix":    []any{"/"},
		"platform_settings": map[string]any{
			"unique_session": true,
		},
		"dashboard": map[string]any{
			"host": "0.0.0.0",
			"port": 6185,
		},
	}
	if err := util.AtomicWriteJSON(cfgPath, minimal, true); err != nil {
		return "", fmt.Errorf("生成 cmd_config.json 失败: %v", err)
	}
	fmt.Printf("[setup] 已自动生成 cmd_config.json: %s\n", cfgPath)
	return cfgPath, nil
}

// setupOneBot 应用 OneBot 配置 (备份 + 原子写) + 重启 AstrBot
func setupOneBot(cfg *config.Config) (string, error) {
	cfgPath, err := ensureCmdConfigExists(cfg)
	if err != nil {
		return "", err
	}
	// 确保白名单插件已安装 (AstrBot 没有内置; /白名单 命令依赖它)
	// 安装成功/已存在 → 返回提示文案; 安装失败 → 返回错误 (OneBot 配置仍继续, 但提示用户)
	pluginMsg, pluginErr := ensureWhitelistPlugin(cfg)
	if pluginErr != nil {
		return "", fmt.Errorf("白名单插件安装失败: %v (可稍后手动放入 %s)", pluginErr, filepath.Join(cfg.AstrbotDataDir, "plugins"))
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
	if pluginMsg != "" {
		return "OneBot 配置已更新; " + pluginMsg, nil
	}
	return "OneBot 配置已更新", nil
}

// ensureWhitelistPlugin 确保 whitelist_manager 插件在 AstrBot 插件目录
// 返回 (msg, err): msg=安装/已存在的提示, err=安装失败
func ensureWhitelistPlugin(cfg *config.Config) (string, error) {
	pluginDir := filepath.Join(cfg.AstrbotDataDir, "plugins", "whitelist_manager")
	mainPy := filepath.Join(pluginDir, "main.py")
	if _, err := os.Stat(mainPy); err == nil {
		return "白名单插件已存在", nil
	}
	// 从内置资产复制
	src, err := assetsFS.ReadFile("assets/whitelist_manager/main.py")
	if err != nil {
		return "", fmt.Errorf("内置插件资源缺失: %v", err)
	}
	srcMeta, _ := assetsFS.ReadFile("assets/whitelist_manager/metadata.yaml")
	_ = os.MkdirAll(pluginDir, 0o755)
	if werr := os.WriteFile(mainPy, src, 0o644); werr != nil {
		return "", werr
	}
	if len(srcMeta) > 0 {
		_ = os.WriteFile(filepath.Join(pluginDir, "metadata.yaml"), srcMeta, 0o644)
	}
	return "已安装白名单插件 (whitelist_manager), 重启 AstrBot 后 /白名单 命令可用", nil
}

// setupPreview 配置变更预览 (不写入)
func setupPreview(cfg *config.Config) map[string]any {
	cfgPath := cfg.Astrbot.CmdConfig
	if _, err := os.Stat(cfgPath); err != nil {
		// 首次使用: cmd_config 尚未生成 → 预览显示"将创建"(不再 404)
		return map[string]any{
			"ok": true, "changes": []string{"cmd_config.json 不存在, 将自动生成最小配置并写入 OneBot 平台"},
			"untouched": []string{"模型 provider (不改动)"},
			"need_restart": true, "cmd_config": cfgPath, "backup_dir": backupDir(),
		}
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
	needRestart := len(changes) > 0
	if len(changes) == 0 {
		changes = append(changes, "无变更")
	}
	return map[string]any{
		"ok": true, "changes": changes, "untouched": []string{"模型 provider (不改动)"},
		"need_restart": needRestart, "cmd_config": cfgPath, "backup_dir": backupDir(),
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