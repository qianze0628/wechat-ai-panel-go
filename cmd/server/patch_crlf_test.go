package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-ai-panel/internal/api"
)

// CRLF 文件场景: AstrBot 在 Windows 上文件多为 CRLF 行尾,
// 验证 patch.go 归一化后能正确打补丁并还原 CRLF。
func TestGroupChatPatchCRLF(t *testing.T) {
	// 构造一个 CRLF 的未打补丁 group_chat_context.py
	base := `import asyncio
from astrbot.api.platform import MessageType
from astrbot.api.platform import MessageType
from astrbot.api.message_components import At, Reply, Plain
from astrbot.api.provider import Provider, ProviderRequest
from astrbot.core.astrbot_config_mgr import AstrBotConfigManager

class GroupChatContext:
    async def handle_message(self, event) -> None:
        if event.get_message_type() != MessageType.GROUP_MESSAGE:
            return
        print("record")
`
	crlf := strings.ReplaceAll(base, "\n", "\r\n")

	tmpPkg := filepath.Join(t.TempDir(), "astrbot")
	builtin := filepath.Join(tmpPkg, "builtin_stars", "astrbot")
	os.MkdirAll(builtin, 0o755)
	target := filepath.Join(builtin, "group_chat_context.py")
	os.WriteFile(target, []byte(crlf), 0o644)

	api.SetAstrbotSitePackages(tmpPkg)

	msg, err := api.ApplyGroupChatPatch()
	if err != nil {
		t.Fatalf("CRLF 打补丁失败: %v", err)
	}
	if !strings.Contains(msg, "已重新打上") {
		t.Fatalf("消息异常: %s", msg)
	}
	out, _ := os.ReadFile(target)
	outStr := string(out)
	if !strings.Contains(outStr, "_is_ai_related") {
		t.Fatal("CRLF 文件打补丁后无标记")
	}
	// 验证仍是 CRLF (未被破坏)
	if !strings.Contains(outStr, "\r\n") || strings.Contains(strings.ReplaceAll(outStr, "\r\n", ""), "\n") {
		t.Fatal("CRLF 文件补丁后换行被破坏")
	}
	t.Logf("PASS: CRLF 文件打补丁成功且换行保持")
}
