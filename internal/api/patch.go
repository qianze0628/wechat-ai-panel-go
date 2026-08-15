// Package api AstrBot 群聊 ICL 补丁自动恢复
//
// 背景: AstrBot 的 group_chat_context.py 会把群里所有消息 (含纯闲聊) 注入
// <system_reminder> 上下文, 导致"答非所问"。我们需要给它打一个 _is_ai_related
// 过滤补丁。但该补丁在 AstrBot 升级/重装 (uv tool install 覆盖 site-packages)
// 后会被冲掉。本模块在面板启动时检测, 缺失则自动重打, 保证升级后群聊效果不退化。
package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ---- 补丁内容 (与 assets/patch/group_chat_context_patch.py 说明一致) ----

// patchImportAnchor 插入点 1: 该 import 行之后插入 _is_ai_related 函数
const patchImportAnchor = "from astrbot.api.platform import MessageType"

// patchFunc 要插入的函数 (缩进必须与文件一致)
const patchFunc = `from astrbot.api.platform import MessageType


# === 群聊 ICL 污染过滤 (面板补丁, AstrBot 升级后会丢失需重新应用) ===
# 问题: handle_message 会把群里所有消息 (含纯闲聊) 注入 <system_reminder>,
#       污染上下文导致"答非所问"。只记录 @了机器人 / 引用机器人 / 机器人自己回复的消息,
#       群闲聊不进上下文但完整保留在 raw_records (可查询)。
def _is_ai_related(event) -> bool:
    # 机器人自己发的内容 (回复/分析) 保留
    if str(event.message_obj.sender.qq) == str(event.get_self_id()):
        return True
    # 引用消息: 引用回复 (通常是对 AI 提问的追问) / @机器人 保留
    for comp in event.get_messages():
        if isinstance(comp, At) and str(comp.qq) in (str(event.get_self_id()), 'all'):
            return True
        if isinstance(comp, Reply):
            return True
    # 群指令词 (如 /白名单) 保留
    text = ''.join(getattr(c, 'text', '') or '' for c in event.get_messages())
    if text.strip().startswith('/'):
        return True
    return False
`

// patchEntryAnchor handle_message 入口: GROUP_MESSAGE 检查后插入过滤
const patchEntryAnchor = `        if event.get_message_type() != MessageType.GROUP_MESSAGE:
            return
`

// patchEntryInsert 插入到 handle_message 入口的过滤代码
const patchEntryInsert = `        if event.get_message_type() != MessageType.GROUP_MESSAGE:
            return

        # 过滤: 只记录 @机器人 / 引用机器人 / 机器人自己回复 / 群指令, 纯闲聊不进上下文
        if not _is_ai_related(event):
            return
`

// patchMarker 检测标记: 文件含此字符串视为已打补丁
const patchMarker = "_is_ai_related"

// ---- whitelist_check 热重载补丁 (2026-08-11) ----
// 背景: WhitelistCheckStage 的 whitelist 只在 initialize 一次性读入内存, 面板 ChatUI 注入/
// 白名单管理器写 cmd_config.json 后必须重启 AstrBot 才生效。本补丁加 mtime 热重载:
// 每次事件前检测配置文件 mtime, 变更则自动重读白名单 → 写入立即生效, 无需重启。
// AstrBot 升级 (uv tool install 覆盖 site-packages) 后会丢失, 由本模块自动重打。

const wlPatchMarker = "_reload_if_changed"

// wlPatchContent 补丁后的完整 stage.py (与当前 AstrBot 版本一致, 升级后可能微调:
// 用 marker 检测, 缺失则重写整个文件并保留逻辑等价)
const wlPatchContent = `from collections.abc import AsyncGenerator
import os
import json as _json

from astrbot.core import logger
from astrbot.core.platform.astr_message_event import AstrMessageEvent
from astrbot.core.platform.message_type import MessageType

from ..context import PipelineContext
from ..stage import Stage, register_stage


@register_stage
class WhitelistCheckStage(Stage):
    """检查是否在群聊/私聊白名单"""

    async def initialize(self, ctx: PipelineContext) -> None:
        self._ctx = ctx
        self.enable_whitelist_check = ctx.astrbot_config["platform_settings"][
            "enable_id_white_list"
        ]
        self._load_whitelist()
        self.wl_ignore_admin_on_group = ctx.astrbot_config["platform_settings"][
            "wl_ignore_admin_on_group"
        ]
        self.wl_ignore_admin_on_friend = ctx.astrbot_config["platform_settings"][
            "wl_ignore_admin_on_friend"
        ]
        self.wl_log = ctx.astrbot_config["platform_settings"]["id_whitelist_log"]
        # 热重载 (2026-08-11): 配置文件 mtime 变化时自动重读白名单, 无需重启 AstrBot
        # 面板 ChatUI 注入/白名单管理器写 cmd_config.json 后立即生效
        self._cfg_path = getattr(ctx.astrbot_config, "config_path", None) or ""
        self._cfg_mtime = self._stat_mtime(self._cfg_path)
        self._reload_count = 0

    def _stat_mtime(self, path: str) -> float:
        try:
            return os.path.getmtime(path)
        except Exception:
            return -1.0

    def _load_whitelist(self):
        raw = self._ctx.astrbot_config["platform_settings"]["id_whitelist"]
        self.whitelist = [
            str(i).strip() for i in raw if str(i).strip() != ""
        ]

    def _reload_if_changed(self):
        """mtime 检测: 配置文件被外部修改(面板/插件)后重读白名单, 立即生效"""
        if not self._cfg_path:
            return
        mtime = self._stat_mtime(self._cfg_path)
        if mtime != self._cfg_mtime:
            self._cfg_mtime = mtime
            try:
                with open(self._cfg_path, encoding="utf-8-sig") as f:
                    data = _json.load(f)
                ps = data.get("platform_settings", {})
                self.enable_whitelist_check = ps.get("enable_id_white_list", self.enable_whitelist_check)
                self._load_whitelist()
                self._reload_count += 1
                if self.wl_log:
                    logger.info(f"[whitelist] 白名单热重载 #{self._reload_count}: 共 {len(self.whitelist)} 项")
            except Exception as e:
                logger.error(f"[whitelist] 热重载失败(忽略, 沿用旧白名单): {e}")

    async def process(
        self,
        event: AstrMessageEvent,
    ) -> None | AsyncGenerator[None, None]:
        # 热重载: 每次事件先检测配置变更
        self._reload_if_changed()
        if not self.enable_whitelist_check:
            # 白名单检查未启用
            return

        if len(self.whitelist) == 0:
            # 白名单为空，不检查
            return

        if event.get_platform_name() == "webchat":
            # WebChat 豁免
            return

        # 检查是否在白名单
        if self.wl_ignore_admin_on_group:
            if (
                event.role == "admin"
                and event.get_message_type() == MessageType.GROUP_MESSAGE
            ):
                return
        if self.wl_ignore_admin_on_friend:
            if (
                event.role == "admin"
                and event.get_message_type() == MessageType.FRIEND_MESSAGE
            ):
                return
        if (
            event.unified_msg_origin not in self.whitelist
            and str(event.get_group_id()).strip() not in self.whitelist
        ):
            if self.wl_log:
                logger.info(
                    f"Session ID {event.unified_msg_origin} is not in the session "
                    "allowlist, so event propagation was stopped. Add this session "
                    "ID to the allowlist in the configuration file.",
                )
            event.stop_event()
`

// ---- 定位 AstrBot site-packages ----

// astrbotSitePackagesOverride 测试/特殊环境可注入固定路径 (优先于自动探测)
var astrbotSitePackagesOverride string

// SetAstrbotSitePackages 注入 astrbot 包目录 (测试用; 也供特殊安装环境租借)
func SetAstrbotSitePackages(dir string) { astrbotSitePackagesOverride = dir }

// astrbotSitePackages 定位 astrbot 包目录 (site-packages), 找不到返回 ""
func astrbotSitePackages() string {
	if astrbotSitePackagesOverride != "" {
		return astrbotSitePackagesOverride
	}
	// 1. uv tools (Windows)
	if runtime.GOOS == "windows" {
		cands := []string{
			filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming", "uv", "tools", "astrbot", "Lib", "site-packages", "astrbot"),
			filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming", "uv", "tools", "astrbot", "lib", "python3.13", "site-packages", "astrbot"),
		}
		for _, c := range cands {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	// 2. 通过 python 命令探测
	if p, err := pythonProbe(); err == nil && p != "" {
		return p
	}
	// 3. D:/python 全局 astrbot
	if runtime.GOOS == "windows" {
		c := filepath.Join("D:", "python", "Lib", "site-packages", "astrbot")
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// pythonProbe 用 python -c 定位 astrbot 包路径 (兼容 win/linux/mac)
func pythonProbe() (string, error) {
	// 修复 (2026-08-15 对抗式审查 中): 原无 Env/超时 — 中文用户名 (zh-CN=GBK/cp936) 时
	// python 输出被 GBK 编码, Go 按 UTF-8 拼接路径必失败 → 补丁静默不恢复。
	// ① 强制 PYTHONIOENCODING=utf-8 ② 30s 超时防挂起。
	probeEnv := append(os.Environ(), "PYTHONIOENCODING=utf-8")
	for _, py := range []string{which2("python"), which2("python3"), which2("uv")} {
		if py == "" {
			continue
		}
		// uv run python -c 更慢, 优先直接 python
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, py, "-c",
			"import astrbot, os; print(os.path.dirname(astrbot.__file__))")
		cmd.Env = probeEnv
		out, err := cmd.Output()
		cancel()
		if err == nil {
			dir := strings.TrimSpace(string(out))
			if dir != "" {
				if _, e := os.Stat(filepath.Join(dir, "builtin_stars", "astrbot", "group_chat_context.py")); e == nil {
					return dir, nil
				}
			}
		}
	}
	return "", fmt.Errorf("未找到 astrbot 包")
}

// groupChatContextPath 目标文件路径
func groupChatContextPath(sitePkg string) string {
	return filepath.Join(sitePkg, "builtin_stars", "astrbot", "group_chat_context.py")
}

// ---- 检测与打补丁 ----

// CheckGroupChatPatch 检测 group_chat_context.py 是否含补丁标记
// 返回 (已打补丁, 文件路径, 错误)
func CheckGroupChatPatch() (bool, string, error) {
	sitePkg := astrbotSitePackages()
	if sitePkg == "" {
		return false, "", fmt.Errorf("未找到 AstrBot 安装目录")
	}
	target := groupChatContextPath(sitePkg)
	data, err := os.ReadFile(target)
	if err != nil {
		return false, target, fmt.Errorf("读取 %s 失败: %v", target, err)
	}
	return strings.Contains(string(data), patchMarker), target, nil
}

// ApplyGroupChatPatch 给 group_chat_context.py 打补丁 (幂等: 已打则跳过)
func ApplyGroupChatPatch() (string, error) {
	patched, target, err := CheckGroupChatPatch()
	if err != nil {
		return "", err
	}
	if patched {
		return "群聊 ICL 补丁已存在 (无需重打)", nil
	}

	data, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("读取失败: %v", err)
	}
	// CRLF 归一化: Windows 上文件多为 CRLF 行尾, 而锚点是 LF 写死的。
	// 统一按 LF 处理逻辑, 写回时若原文件是 CRLF 则还原 CRLF (避免混换行)。
	crlf := strings.Contains(string(data), "\r\n")
	src := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.Contains(src, "\n") && len(src) > 0 {
		// 单行文件兜底
		src = string(data)
	}

	// 1. 插入函数
	if !strings.Contains(src, patchImportAnchor) {
		return "", fmt.Errorf("未找到 import 锚点 (%s), 可能 AstrBot 结构已变, 请手动打补丁", patchImportAnchor)
	}
	src = strings.Replace(src, patchImportAnchor, patchFunc, 1)

	// 2. 插入 handle_message 过滤
	if !strings.Contains(src, patchEntryAnchor) {
		return "", fmt.Errorf("未找到 handle_message 锚点, 可能 AstrBot 结构已变, 请手动打补丁")
	}
	src = strings.Replace(src, patchEntryAnchor, patchEntryInsert, 1)

	// 3. 备份原文件 (一次)
	_ = os.WriteFile(target+".panel.bak", data, 0o644)

	// 4. 写回 (还原原行尾)
	out := []byte(src)
	if crlf {
		out = []byte(strings.ReplaceAll(src, "\n", "\r\n"))
	}
	if err := os.WriteFile(target, out, 0o644); err != nil {
		return "", fmt.Errorf("写入失败: %v", err)
	}
	return "已重新打上群聊 ICL 补丁 (group_chat_context.py)", nil
}

// ---- 启动时后台检查 (面板启动即跑, 幂等) ----

// autoPatchOnce 启动后延迟几秒检查一次; 只打一次 (用 sync.Once 语义弱化)
var patchOnceDone = make(chan struct{}, 1)

// startAstrbotPatchWatcher 在 main.go 调用, 启动时后台检测补丁
func EnsureGroupChatPatch() {
	go func() {
		defer func() { _ = recover() }()
		// 等 AstrBot 可能正在更新 (避免启动瞬间文件不稳定)
		time.Sleep(3 * time.Second)
		select {
		case <-patchOnceDone:
			return
		default:
		}
		defer func() { patchOnceDone <- struct{}{} }()
		msg, err := ApplyGroupChatPatch()
		if err != nil {
			fmt.Printf("[patch] 群聊 ICL 补丁检查失败: %v\n", err)
			return
		}
		fmt.Printf("[patch] %s\n", msg)
	}()
}

// ---- 手动触发 (面板向后兼容, 供日志/检查) ----

// PatchStatus 补丁状态结构 (给前端/日志用)
type PatchStatus struct {
	Applied bool   `json:"applied"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// RegisterPatch 注册补丁状态/重打 API (供日志页与诊断用)
//   - GET  /api/patch/status  查询群聊 ICL 补丁状态
//   - POST /api/patch/reapply 手动触发重打 (返回是否重新打过)
func (h *Handler) RegisterPatch() {
	h.mux.HandleFunc("/api/patch/status", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, groupChatPatchStatus())
	})
	h.mux.HandleFunc("/api/patch/reapply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonErr(w, 405, "仅支持 POST")
			return
		}
		msg, err := ApplyGroupChatPatch()
		if err != nil {
			jsonErr(w, 500, err.Error())
			return
		}
		jsonOK(w, map[string]any{"ok": true, "message": msg, "status": groupChatPatchStatus()})
	})
}

// groupChatPatchStatus 查询状态
func groupChatPatchStatus() PatchStatus {
	patched, target, err := CheckGroupChatPatch()
	if err != nil {
		return PatchStatus{Applied: false, Path: "", Message: err.Error()}
	}
	msg := "未打补丁 (upgrade 后会丢)"
	if patched {
		msg = "已打补丁"
	}
	return PatchStatus{Applied: patched, Path: target, Message: msg}
}