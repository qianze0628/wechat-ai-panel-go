package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"wechat-ai-panel/internal/api"
)

// 模拟 AstrBot 升级 (补丁丢失) → 自动重打 → 验证。
// 还原用精确行级过滤: 只删补丁内容和过滤调用, 不碰其他逻辑。
func TestGroupChatPatchAutoReapply(t *testing.T) {
	realTarget := filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming", "uv", "tools", "astrbot", "Lib", "site-packages", "astrbot", "builtin_stars", "astrbot", "group_chat_context.py")
	data, err := os.ReadFile(realTarget)
	if err != nil {
		t.Skipf("本机无 AstrBot: %v", err)
	}
	// CRLF→LF 归一化 (与 patch.go 一致)
	src := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.Contains(src, "_is_ai_related") {
		t.Fatal("真实文件应含补丁标记")
	}

	// 精确还原: 1) 删除 _is_ai_related 函数块 (从注释头到尾行 return False, 允许缩进)
	reFn := regexp.MustCompile(`(?ms)^# === 群聊 ICL 污染过滤.*?^\s*return False\n`)
	if !reFn.MatchString(src) {
		t.Fatal("未匹配到 _is_ai_related 函数块")
	}
	src = reFn.ReplaceAllString(src, "")

	// 2) 删除 handle_message 入口的过滤两行
	reFilter := regexp.MustCompile(`(?m)^        # 过滤: 只记录 @机器人.*?\n        if not _is_ai_related\(event\):\n            return\n`)
	if !reFilter.MatchString(src) {
		t.Fatal("未匹配到过滤调用块")
	}
	src = reFilter.ReplaceAllString(src, "")

	if strings.Contains(src, "_is_ai_related") {
		t.Fatal("还原后仍含补丁标记")
	}

	// 构造临时 astrbot 包目录
	tmpPkg := filepath.Join(t.TempDir(), "astrbot")
	builtin := filepath.Join(tmpPkg, "builtin_stars", "astrbot")
	os.MkdirAll(builtin, 0o755)
	target := filepath.Join(builtin, "group_chat_context.py")
	if err := os.WriteFile(target, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	api.SetAstrbotSitePackages(tmpPkg)

	// 检测 (未打) → 重打 → 再检测
	patched, _, err := api.CheckGroupChatPatch()
	if err != nil || patched {
		t.Fatalf("初始检测异常: patched=%v err=%v", patched, err)
	}
	msg, err := api.ApplyGroupChatPatch()
	if err != nil {
		t.Fatalf("重打失败: %v", err)
	}
	if !strings.Contains(msg, "已重新打上") {
		t.Fatalf("重打消息异常: %s", msg)
	}
	patched2, _, _ := api.CheckGroupChatPatch()
	if !patched2 {
		t.Fatal("重打后仍未检测到补丁")
	}
	// 幂等
	msg2, _ := api.ApplyGroupChatPatch()
	if !strings.Contains(msg2, "已存在") {
		t.Fatalf("幂等失败: %s", msg2)
	}
	t.Logf("PASS: 升级→自动重打→幂等 全通过")
}
