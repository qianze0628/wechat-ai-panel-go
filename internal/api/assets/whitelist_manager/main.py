# 微信联系人白名单管理器 - AstrBot 插件
# 功能:
#   /白名单          -> 查看当前白名单 + 联系人/群列表
#   /白名单 添加 <名字> -> 把联系人/群加入聊天白名单
#   /白名单 移除 <名字> -> 从聊天白名单移除
#   /管理员 设置 <名字> -> 设为管理员(可访问电脑)
#   /管理员 移除 <名字> -> 移除管理员
# 数据源: wechat-bot 的 HTTP API (端口 6189), 提供真实联系人/群的 hashId

import json
import asyncio
import os
import urllib.request
from pathlib import Path

from astrbot.api import logger
from astrbot.api.event import filter, AstrMessageEvent
from astrbot.api.star import Context, Star, register

WECHAT_API_BASE = os.environ.get("WECHAT_API_BASE", "http://127.0.0.1:6189")
ASTRBOT_CONFIG = Path.home() / "data" / "cmd_config.json"
WECHAT_BOT_ENV = Path(os.environ.get("WECHAT_BOT_ENV", r"C:\Users\YMB\Desktop\wechat\wechat-bot-windows\.env"))

def _hash_name(s: str) -> str:
    """与 wechat-bot bridge-integration.js 的 hashId() 完全一致的算法 (JS 字符哈希+10000)
    用于把 白名单里的 hashId(名字) 反查成联系人名字"""
    if not s:
        s = "unknown"
    h = 0
    for ch in s:
        h = ((h << 5) - h + ord(ch)) & 0xFFFFFFFF  # JS 32位有符号溢出
        if h >= 0x80000000:
            h -= 0x100000000
    return str(abs(h) + 10000)

def _build_id_name_map(contacts, rooms):
    """构建 hashId -> 名字 双向映射:
    key1 = hashId(微信原始ID)  (6189 API 返回的 hashId)
    key2 = hashId(名字/备注名)  (白名单里存的数字)
    key3 = hashId(微信名)       (rawName, 可能和备注名不同)
    同时处理 wechat-bridge:FriendMessage:xxx / wechat-bridge:GroupMessage:xxx 老格式"""
    id_to_name = {}
    for c in contacts:
        nm = c.get("name", "")
        alias = c.get("alias", "")
        raw = c.get("rawName", "")
        raw_id = str(c.get("id", ""))
        # 每个来源各自映射到它自己的名字 (备注名/微信名分开, 避免互相覆盖)
        id_to_name[_hash_name(nm)] = nm if nm else (raw or "未知")
        if alias and alias != nm:
            id_to_name[_hash_name(alias)] = alias
        if raw and raw != nm:
            id_to_name[_hash_name(raw)] = raw
        if raw_id:
            id_to_name[_hash_name(raw_id)] = nm or raw or "未知"
        # 微信原始ID 本身 (API hashId)
        id_to_name[str(c.get("hashId", ""))] = nm or raw or "未知"
    for r in rooms:
        nm = r.get("name", "")
        raw_id = str(r.get("id", ""))
        for key in (raw_id, nm):
            if key:
                id_to_name[_hash_name(key)] = f'[群]{nm or key}'
        id_to_name[str(r.get("hashId", ""))] = f'[群]{nm or raw_id}'
    return id_to_name

def _read_config():
    raw = ASTRBOT_CONFIG.read_text(encoding="utf-8-sig")
    return json.loads(raw)

def _write_config(cfg):
    ASTRBOT_CONFIG.write_text(json.dumps(cfg, ensure_ascii=False, indent=2), encoding="utf-8")

def _current_whitelist():
    cfg = _read_config()
    ps = cfg.get("platform_settings", {})
    return {
        "enabled": ps.get("enable_id_white_list", False),
        "chatIds": [str(x) for x in ps.get("id_whitelist", [])],
        "adminIds": [str(x) for x in cfg.get("admins_id", []) if str(x) != "astrbot"],
    }

def _fetch_contacts():
    """从 wechat-bot 拉联系人/群列表"""
    try:
        req = urllib.request.Request(
            f"{WECHAT_API_BASE}/api/contacts",
            headers={"User-Agent": "astrbot-whitelist-plugin"},
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except Exception as e:
        return {"error": str(e), "contacts": [], "rooms": []}

def _find_target(contacts, rooms, keyword):
    """精准匹配: 名字/备注/微信名 完全相等 或 hashId 精确相等.
    模糊匹配(包含/子串) 一律走 _find_candidates 由用户确认, 避免乱加. """
    kw = keyword.strip()
    for c in contacts:
        cname = c.get("name", "")
        calias = c.get("alias", "")
        craw = c.get("rawName", "")
        if kw == cname or kw == calias or kw == craw or kw == str(c.get("hashId", "")):
            return "联系人", cname, c["hashId"], (craw or cname)
    for r in rooms:
        if kw == r.get("name", "") or kw == str(r.get("hashId", "")):
            return "群聊", r["name"], r["hashId"], ""
    return None, None, None, None

def _find_candidates(contacts, rooms, keyword):
    """返回与 keyword 匹配的全部候选, 带置信度 level:
    [(类型, 展示名, hashId, rawName, level)]
      level=2 名字完整包含 keyword (如 输入"任凯雯"→ 无, 因字不同)
      level=1 名字是 keyword 的小段子串 (如 "凯" 是 "任凯雯" 子串, 低置信)
      "名字等于 keyword" 已在 _find_target 精准匹配处理, 这里不含
    只有当存在 level=2 (名字完整包含关键词) 的候选时才认为'像那么回事';
    仅剩 level=1 子串命中时置信太低, 一律询问用户, 严禁直接添加 (会乱加如 '凯')."""
    kw = keyword.strip()
    cands = []
    for c in contacts:
        cname = c.get("name", "")
        craw = c.get("rawName", "")
        calias = c.get("alias", "")
        for n in (cname, craw, calias):
            if not n or n.strip() == kw:
                continue  # 完全相等已由 _find_target 处理
            if n and kw in n:
                cands.append(("联系人", cname or craw, c["hashId"], craw or cname, 2))
                break
            elif len(kw) >= 3 and n in kw and len(n) >= 2:
                # "凯" 这种短名是整词的一部分, 低置信 (仅当 kw≥3 字)
                cands.append(("联系人", cname or craw, c["hashId"], craw or cname, 1))
                break
    for r in rooms:
        rn = r.get("name", "")
        if not rn or rn.strip() == kw:
            continue
        if kw in rn:
            cands.append(("群聊", rn, r["hashId"], "", 2))
        elif len(kw) >= 3 and rn in kw and len(rn) >= 2:
            cands.append(("群聊", rn, r["hashId"], "", 1))
    # level 高的在前 (2 优先, 1 垫底), 名字短平
    cands.sort(key=lambda x: (-x[4], -len(x[1])))
    # 去重 (同 hashId, 保留最高 level)
    seen = {}
    out = []
    for c in cands:
        if c[2] not in seen or seen[c[2]][4] < c[4]:
            seen[c[2]] = c
    out = sorted(seen.values(), key=lambda x: (-x[4], -len(x[1])))
    return out


def _read_env_names() -> list:
    """读取 .env 当前的 ALIAS_WHITELIST"""
    try:
        if not WECHAT_BOT_ENV.exists():
            return []
        import re
        content = WECHAT_BOT_ENV.read_text(encoding="utf-8")
        m = re.search(r"ALIAS_WHITELIST='([^']*)'", content)
        return [x.strip() for x in m.group(1).split(',') if x.strip()] if m else []
    except Exception:
        return []

def _read_env_rooms() -> list:
    """读取 .env 当前的 ROOM_WHITELIST"""
    try:
        if not WECHAT_BOT_ENV.exists():
            return []
        import re
        content = WECHAT_BOT_ENV.read_text(encoding="utf-8")
        m = re.search(r"ROOM_WHITELIST='([^']*)'", content)
        return [x.strip() for x in m.group(1).split(',') if x.strip()] if m else []
    except Exception:
        return []

def _read_env_exclude() -> dict:
    """读取 .env 当前的 ROOM_MEMBER_EXCLUDE: {群名: [成员名...]}"""
    try:
        if not WECHAT_BOT_ENV.exists():
            return {}
        import re
        content = WECHAT_BOT_ENV.read_text(encoding="utf-8")
        m = re.search(r"ROOM_MEMBER_EXCLUDE='([^']*)'", content)
        raw = m.group(1) if m else ""
        out = {}
        for seg in raw.split(";"):
            if ":" not in seg:
                continue
            room, members = seg.split(":", 1)
            room = room.strip()
            ms = [x.strip() for x in members.split(",") if x.strip()]
            if room and ms:
                out[room] = ms
        return out
    except Exception:
        return {}

def _update_wechat_bot_env(names: list, rooms: list, exclude_map: dict = None):
    """更新 wechat-bot 的 .env 白名单 (按名字, 真正生效的拦截)

    exclude_map: {群名: [成员名...]} → 写入 ROOM_MEMBER_EXCLUDE (群内屏蔽成员)
    """
    try:
        if not WECHAT_BOT_ENV.exists():
            logger.warning(f"wechat-bot .env 不存在: {WECHAT_BOT_ENV}")
            return
        content = WECHAT_BOT_ENV.read_text(encoding="utf-8")
        import re
        content = re.sub(r"ALIAS_WHITELIST='[^']*'", f"ALIAS_WHITELIST='{','.join(names)}'", content)
        content = re.sub(r"ROOM_WHITELIST='[^']*'", f"ROOM_WHITELIST='{','.join(rooms)}'", content)
        # 群内屏蔽成员: 格式 '群名:成员A,成员B;另一群:成员C'
        exclude_str = ""
        if exclude_map:
            parts = []
            for room, ms in exclude_map.items():
                if ms:
                    parts.append(f"{room}:{','.join(ms)}")
            exclude_str = ";".join(parts)
        if re.search(r"ROOM_MEMBER_EXCLUDE='[^']*'", content):
            content = re.sub(r"ROOM_MEMBER_EXCLUDE='[^']*'", f"ROOM_MEMBER_EXCLUDE='{exclude_str}'", content)
        else:
            # .env 无该字段时追加 (放在 ROOM_WHITELIST 行后)
            content = re.sub(
                r"(ROOM_WHITELIST='[^']*')",
                lambda m: m.group(1) + f"\nROOM_MEMBER_EXCLUDE='{exclude_str}'",
                content,
                count=1,
            ) if "ROOM_WHITELIST" in content else (content + f"\nROOM_MEMBER_EXCLUDE='{exclude_str}'\n")
        WECHAT_BOT_ENV.write_text(content, encoding="utf-8")
        logger.info(f"wechat-bot .env 已更新: 联系人={names} 群={rooms} 群内屏蔽={exclude_map or {}}")
    except Exception as e:
        logger.warning(f"更新 wechat-bot .env 失败: {e}")

def _restart_wechat_bot_async():
    """重启 wechat-bot 让 .env 白名单生效 (登录态保留, 免扫码)"""
    import threading
    def do_restart():
        import time
        time.sleep(2)
        try:
            import subprocess
            # 杀掉旧 wechat-bot 进程
            subprocess.run(["powershell", "-Command",
                "Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -match 'cli.js start' -and $_.Name -eq 'node.exe' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }"],
                capture_output=True, timeout=10)
            time.sleep(3)
            # 启动新 wechat-bot
            subprocess.Popen(["cmd.exe", "/c", "cd /d C:\\Users\\YMB\\Desktop\\wechat\\wechat-bot-windows && node cli.js start -s ChatGPT > C:\\Users\\YMB\\Desktop\\wechat\\wechatbot-win.log 2>&1"],
                creationflags=0x08000000)  # CREATE_NO_WINDOW
            logger.info("wechat-bot 已重启 (白名单生效)")
        except Exception as e:
            logger.warning(f"重启 wechat-bot 失败: {e}")
    threading.Thread(target=do_restart, daemon=True).start()

def _restart_astrbot_async():
    """保存白名单后自动重启 AstrBot, 让白名单立即生效 (无需手动重启)"""
    import threading
    def do_restart():
        import time
        time.sleep(2)  # 先让回复发出
        try:
            import urllib.request
            # 1. 登录拿 JWT
            login_body = json.dumps({"username": "astrbot", "password": "astrbot123456"}).encode()
            login_req = urllib.request.Request(
                "http://127.0.0.1:6185/api/v1/auth/login",
                data=login_body,
                headers={"Content-Type": "application/json"},
            )
            token = json.loads(urllib.request.urlopen(login_req, timeout=5).read())["data"]["token"]
            # 2. 触发重启
            req = urllib.request.Request(
                "http://127.0.0.1:6185/api/v1/system/restart",
                method="POST",
                headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
            )
            urllib.request.urlopen(req, timeout=5)
            logger.info("whitelist_manager: 已触发 AstrBot 重启使白名单生效")
        except Exception as e:
            logger.warning(f"whitelist_manager: 自动重启失败(可手动重启): {e}")
    threading.Thread(target=do_restart, daemon=True).start()

@register("whitelist_manager", "you", "微信联系人白名单管理器", "1.0.0")
class WhitelistManager(Star):
    def __init__(self, context: Context, config: dict = None):
        super().__init__(context)
        # 待确认的模糊匹配候选 (联系人名字列表); 用户回复序号后二选
        self._pending_candidates = None
        self._pending_kind = None  # "add" / "remove"
        # ===== 注册 Web API (通过 AstrBot WebUI 访问) =====
        # 访问: http://localhost:6185/api/plug/whitelist_manager/ui
        try:
            context.register_web_api(
                "/whitelist_manager/ui",
                self._web_ui,
                ["GET"],
                "白名单管理器界面",
            )
            context.register_web_api(
                "/whitelist_manager/contacts",
                self._web_contacts,
                ["GET"],
                "微信联系人/群列表",
            )
            context.register_web_api(
                "/whitelist_manager/whitelist",
                self._web_whitelist,
                ["GET", "POST"],
                "白名单读写",
            )
            logger.info("whitelist_manager: Web API 已注册")
        except Exception as e:
            logger.error(f"whitelist_manager: Web API 注册失败: {e}")

    # ===== Web API 处理函数 (通过 asgi_runtime 的 request 代理访问当前请求) =====
    async def _web_ui(self, **kwargs):
        """返回白名单管理器 HTML 界面"""
        from starlette.responses import HTMLResponse
        return HTMLResponse(_UI_HTML)

    async def _web_contacts(self, **kwargs):
        """返回联系人/群列表"""
        return _fetch_contacts()

    async def _web_whitelist(self, **kwargs):
        """GET: 返回当前白名单; POST: 保存白名单"""
        from starlette.responses import JSONResponse
        from astrbot.dashboard.asgi_runtime import request
        if request.method == "GET":
            out = _current_whitelist()
            # 群内屏蔽名单 (回显给前端): 两种形态都返回, 前端优先用 hashId 映射, 缺失时用群名+成员名
            #   excludedGroupMembers: {群hashId: [成员hashId]} (从 contacts 尽力映射)
            #   excludedGroupNames:   {群名: [成员名]}         (直接从 .env, 不依赖 bot 群列表)
            try:
                excl = _read_env_exclude()
                out["excludedGroupNames"] = excl
                excl_out = {}
                data = _fetch_contacts()
                if not data.get("error"):
                    rooms_by_hash = {str(r["hashId"]): r["name"] for r in data.get("rooms", [])}
                    name_by_hash = {}
                    for r in data.get("rooms", []):
                        for m in r.get("members", []) or []:
                            nm = m.get("name", "") or ""
                            if nm and nm != "未知名成员":
                                name_by_hash[nm] = str(m.get("hashId", ""))
                    for rname, ms in excl.items():
                        rhid = next((k for k, v in rooms_by_hash.items() if v == rname), None)
                        if not rhid:
                            continue
                        mhids = [name_by_hash.get(nm) for nm in ms if name_by_hash.get(nm)]
                        if mhids:
                            excl_out[str(rhid)] = mhids
                out["excludedGroupMembers"] = excl_out
            except Exception as e:
                logger.warning(f"读取群内屏蔽名单失败: {e}")
            return out
        # POST: 多来源读取 body (原始体 / request.json / kwargs / form / query 都兼容)
        body = {}
        # 0) 原始 body (不依赖 Content-Type 解析, 最可靠)
        try:
            raw = await request.get_data()
            if raw:
                import json as _json
                candidate = _json.loads(raw.decode("utf-8"))
                if isinstance(candidate, dict):
                    body = candidate
        except Exception:
            body = body or {}
        # 1) request.json()
        if not body.get("chatIds"):
            try:
                candidate = await request.json()
                if isinstance(candidate, dict):
                    body = candidate
            except Exception:
                body = body or {}
        # 2) kwargs (SDK 路径注入)
        if not body.get("chatIds") and kwargs.get("chatIds"):
            body = {"chatIds": kwargs.get("chatIds"), "adminIds": kwargs.get("adminIds", body.get("adminIds"))}
        # 3) form 兜底
        if not body.get("chatIds"):
            try:
                form = await request.form()
                if isinstance(form, dict) and form.get("chatIds"):
                    body = {"chatIds": [x for x in str(form.get("chatIds")).split(",") if x], "adminIds": [x for x in str(form.get("adminIds", "")).split(",") if x]}
            except Exception:
                pass
        # 4) 查询参数兜底 (DashboardRequest 用 .args)
        if not body.get("chatIds"):
            try:
                args = request.args
                if args is not None and str(args.get("chatIds", "")):
                    body = {"chatIds": [x for x in str(args.get("chatIds")).split(",") if x], "adminIds": [x for x in str(args.get("adminIds", "")).split(",") if x]}
            except Exception:
                pass
        # 保护: 若确实拿不到白名单列表, 拒绝安全地拒绝保存, 避免清空配置
        if not body.get("chatIds"):
            return JSONResponse({"status": "error", "message": "未解析到白名单列表 (chatIds), 已取消保存，请重试", "error": "missing chatIds"})
        chat_ids = [str(x) for x in body.get("chatIds", [])]
        admin_ids = [str(x) for x in body.get("adminIds", [])]
        cfg = _read_config()
        ps = cfg.setdefault("platform_settings", {})
        ps["enable_id_white_list"] = len(chat_ids) > 0
        ps["id_whitelist"] = chat_ids
        cfg["admins_id"] = admin_ids if admin_ids else ["astrbot"]
        _write_config(cfg)
        # 同步 wechat-bot .env 白名单 (按名字) + 重启 wechat-bot
        synced = False
        try:
            data = _fetch_contacts()
            if not data.get("error"):
                contacts_by_hash = {str(c["hashId"]): (c.get("name", ""), c.get("rawName", "")) for c in data.get("contacts", [])}
                rooms_by_hash = {str(r["hashId"]): r["name"] for r in data.get("rooms", [])}
                # 群内屏蔽名单: {群hashId: [成员hashId]} → {群名: [成员名]}
                excl_body = body.get("excludedGroupMembers") or {}
                member_name_by_hash = {}
                for r in data.get("rooms", []):
                    for m in r.get("members", []) or []:
                        nm = m.get("name", "") or ""
                        if nm and nm != "未知名成员":
                            member_name_by_hash[str(m.get("hashId", ""))] = nm
                exclude_map = {}
                for rhid, mhids in excl_body.items():
                    if not isinstance(mhids, list):
                        continue
                    rname = rooms_by_hash.get(str(rhid))
                    if not rname:
                        continue
                    ms = []
                    for mhid in mhids:
                        nm = member_name_by_hash.get(str(mhid))
                        if nm:
                            ms.append(nm)
                    if ms:
                        exclude_map[rname] = list(dict.fromkeys(ms))
                names = []
                for x in chat_ids:
                    if x in contacts_by_hash:
                        nm, raw = contacts_by_hash[x]
                        if nm: names.append(nm)
                        if raw and raw != nm: names.append(raw)
                names = list(dict.fromkeys(names))
                rooms = list(dict.fromkeys(rooms_by_hash[x] for x in chat_ids if x in rooms_by_hash))
                _update_wechat_bot_env(names, rooms, exclude_map)
                _restart_wechat_bot_async()
                synced = True
        except Exception as e:
            logger.warning(f"WebUI 保存时同步桥接白名单失败: {e}")
        return JSONResponse({
            "status": "ok",
            "message": "白名单已保存" + ("，已同步桥接并重启 wechat-bot 生效" if synced else "（桥接同步失败，仅 AstrBot 生效）"),
            "chatIds": chat_ids,
            "adminIds": admin_ids,
        })

    @filter.command("白名单")
    @filter.permission_type(filter.PermissionType.ADMIN)
    async def whitelist(self, event: AstrMessageEvent, arg1: str = ""):
        """查看白名单状态"""
        data = _fetch_contacts()
        if data.get("error"):
            yield event.plain_result(
                f"⚠️ 无法连接微信 bot: {data['error']}\n"
                "请确认 wechat-bot 已登录并运行（端口6189）"
            )
            return

        cfg = _read_config()
        ps = cfg.get("platform_settings", {})
        chat_ids = [str(x) for x in ps.get("id_whitelist", [])]
        admin_ids = [str(x) for x in cfg.get("admins_id", [])]

        enabled = ps.get("enable_id_white_list", False)

        # 构建 hashId(名字/原始ID/备注名) -> 名字 双向映射, 兼容老格式 wechat-bridge:FriendMessage:xxx
        id_to_name = _build_id_name_map(data.get("contacts", []), data.get("rooms", []))

        # 构建"联系人ID -> 名字"的映射, 用于把备注名/微信名合并成一个人
        # person_map: 主名字 -> 附加名(微信名), 用于去重显示
        contact_by_hash = {}  # hashId -> {name(备注), raw(微信名), hash}
        for c in data.get("contacts", []):
            ch = str(c.get("hashId", ""))
            contact_by_hash[ch] = {
                "name": c.get("name", "") or c.get("rawName", ""),
                "raw": c.get("rawName", ""),
                "hash": ch,
            }

        def resolve(x):
            if ":" in x:
                # wechat-bridge:FriendMessage:24689637 -> 取末尾数字
                suffix = x.rsplit(":", 1)[-1]
                if suffix and suffix in id_to_name:
                    return id_to_name[suffix]
                return f"{suffix}(桥接ID)" if suffix else x
            return id_to_name.get(x, x)

        # 去重: 同一个人(备注名+微信名哈希)只显示一次, 优先显示备注名, 微信名作为附注
        # person_key: 用联系人 hashId(原始ID) 关联, 备注名/微信名哈希都归到同一个人
        # 建立 "解析出的名字" -> 联系人 hashId 的映射, 用于合并
        name_to_contact = {}  # 名字 -> contact hashId
        for c in data.get("contacts", []):
            nm = c.get("name", "")
            raw = c.get("rawName", "")
            ch = str(c.get("hashId", ""))
            for n in (nm, raw):
                if n and n not in name_to_contact:
                    name_to_contact[n] = ch
                elif n and name_to_contact.get(n) != ch:
                    # 同名不同人, 不合并
                    pass

        resolved = [(x, resolve(x)) for x in chat_ids]

        # 合并显示: 备注名 + (微信名) 只显示一次
        shown = []
        seen_people = set()  # 联系人 hashId
        seen_names = set()
        for x, rn in resolved:
            if rn.startswith("["):  # 群聊
                key = rn
            else:
                ch = name_to_contact.get(rn)
                key = ch if ch else rn
            if key in seen_people or rn in seen_names:
                continue  # 同一人已显示
            seen_people.add(key)
            seen_names.add(rn)
            shown.append(rn)

        lines = []
        lines.append(f"📋 白名单状态: {'启用' if enabled else '未启用(全部可聊)'}")
        if chat_ids:
            lines.append("💬 聊天白名单:")
            lines.extend(f"   - {n}" for n in shown)
        if admin_ids:
            anames = [resolve(x) for x in admin_ids if x != "astrbot"]
            lines.append("🛡️ 管理员(可访问电脑):")
            lines.extend(f"   - {n}" for n in anames)

        lines.append("")
        lines.append(f"📇 当前联系人 (共{len(data.get('contacts', []))}个, 显示前30):")
        for c in data.get("contacts", [])[:30]:
            mark = "✅" if str(c["hashId"]) in chat_ids else "  "
            lines.append(f"  {mark} {c['name']} ({c['hashId']})")
        lines.append(f"📇 群聊 (共{len(data.get('rooms', []))}个):")
        for r in data.get("rooms", []):
            mark = "✅" if str(r["hashId"]) in chat_ids else "  "
            lines.append(f"  {mark} [群]{r['name']} ({r['hashId']})")

        yield event.plain_result("\n".join(lines))

    @filter.command("白名单添加")
    @filter.permission_type(filter.PermissionType.ADMIN)
    async def whitelist_add(self, event: AstrMessageEvent, arg1: str = ""):
        """把联系人/群加入聊天白名单"""
        if not arg1:
            yield event.plain_result("用法: /白名单添加 <联系人名或群名>")
            return
        keyword = arg1
        data = _fetch_contacts()
        if data.get("error"):
            yield event.plain_result(f"⚠️ 无法连接微信 bot: {data['error']}")
            return
        # 0) 若之前在等待用户回复序号 (候选确认), 且本次输入是数字 → 按序号选
        if self._pending_candidates and keyword.strip().isdigit():
            idx = int(keyword.strip()) - 1
            cands = self._pending_candidates
            self._pending_candidates = None
            if not (0 <= idx < len(cands)):
                yield event.plain_result(f"❌ 序号无效 (1-{len(cands)})，请重新 /白名单添加。")
                return
            c = cands[idx]
            typ, name, hid, raw_name = c[0], c[1], c[2], c[3]
        elif keyword.strip() in ("取消", "skip", "跳过"):
            self._pending_candidates = None
            yield event.plain_result("已取消模糊匹配。请使用精确名字/ID 重新输入。")
            return
        else:
            # 1) 精准匹配优先: 名字完全相等 或 hashId 精确相等
            typ, name, hid, raw_name = _find_target(data.get("contacts", []), data.get("rooms", []), keyword)

            # 2) 精准未命中: 一律不自动添加 (防误加).
            #    把模糊/局部匹配到的候选全部列出让用户选择; 无可选则明确报错.
            if not hid:
                cands = _find_candidates(data.get("contacts", []), data.get("rooms", []), keyword)
                if cands:
                    self._pending_candidates = cands
                    lines = [f"⚠️ 没有与 '{keyword}' 完全匹配的名字。以下为局部/模糊候选, 请回复序号确认 (或 回复'取消'):"]
                    for i, c in enumerate(cands, 1):
                        mark = "✅" if c[4] >= 2 else "  "
                        lines.append(f"{i}. {mark} [{c[0]}] {c[1]} ({c[2]})")
                    yield event.plain_result("\n".join(lines))
                    return
                else:
                    yield event.plain_result(
                        f"❌ 找不到与 '{keyword}' 精准匹配的联系人或群, 也没有局部候选。请检查名字后重试。"
                    )
                    return

        # 同时更新 AstrBot 配置 (备份) 和 wechat-bot .env (真正生效的按名字白名单)
        cfg = _read_config()
        ps = cfg.setdefault("platform_settings", {})
        wl = [str(x) for x in ps.get("id_whitelist", [])]
        # 添加所有 ID 形式（API哈希 / 会话哈希 / 完整UMO），确保 AstrBot 白名单检查能匹配
        add_ids = [str(hid)]
        if typ == "联系人":
            for nm in [name, raw_name]:
                if nm:
                    h = _hash_name(nm)
                    add_ids += [h, f"wechat-bridge:FriendMessage:{h}"]
        else:
            h = _hash_name(name)
            add_ids += [h, f"wechat-bridge:GroupMessage:{h}"]
        for x in add_ids:
            if x not in wl:
                wl.append(x)
        ps["id_whitelist"] = wl
        _write_config(cfg)
        # 更新 wechat-bot .env 白名单 (备注名+名字双写, 按名字) + 重启 wechat-bot
        if typ == "联系人":
            names = list(dict.fromkeys(_read_env_names() + [name, raw_name]))
            _update_wechat_bot_env(names, _read_env_rooms())
            _restart_wechat_bot_async()
            yield event.plain_result(f"✅ 已添加联系人『{name}』到白名单，正在重启 wechat-bot 生效...")
        else:
            rooms = list(dict.fromkeys(_read_env_rooms() + [name]))
            _update_wechat_bot_env(_read_env_names(), rooms)
            _restart_wechat_bot_async()
            yield event.plain_result(f"✅ 已添加群聊『{name}』到白名单，正在重启 wechat-bot 生效...")

    @filter.command("白名单移除")
    @filter.permission_type(filter.PermissionType.ADMIN)
    async def whitelist_remove(self, event: AstrMessageEvent, arg1: str = ""):
        """把联系人/群移出聊天白名单"""
        if not arg1:
            yield event.plain_result("用法: /白名单移除 <联系人名或群名>")
            return
        keyword = arg1
        data = _fetch_contacts()
        if data.get("error"):
            yield event.plain_result(f"⚠️ 无法连接微信 bot: {data['error']}")
            return
        # 候选确认状态: 回复序号
        if self._pending_candidates and keyword.strip().isdigit():
            idx = int(keyword.strip()) - 1
            cands = self._pending_candidates
            self._pending_candidates = None
            if not (0 <= idx < len(cands)):
                yield event.plain_result(f"❌ 序号无效 (1-{len(cands)})")
                return
            c = cands[idx]
            typ, name, hid, raw_name = c[0], c[1], c[2], c[3]
        else:
            typ, name, hid, raw_name = _find_target(data.get("contacts", []), data.get("rooms", []), keyword)
            if not hid:
                cands = _find_candidates(data.get("contacts", []), data.get("rooms", []), keyword)
                if cands:
                    self._pending_candidates = cands
                    lines = [f"⚠️ 没有与 '{keyword}' 完全匹配的名字。以下为局部/模糊候选, 请回复序号确认 (或 回复'取消'):"]
                    for i, c in enumerate(cands, 1):
                        mark = "✅" if c[4] >= 2 else "  "
                        lines.append(f"{i}. {mark} [{c[0]}] {c[1]} ({c[2]})")
                    yield event.plain_result("\n".join(lines))
                    return
                else:
                    yield event.plain_result(f"❌ 找不到与 '{keyword}' 精准匹配的联系人或群。")
                    return
        cfg = _read_config()
        ps = cfg.setdefault("platform_settings", {})
        # 构建该联系/群的所有 ID 形式并全部删除（API哈希/会话哈希/完整UMO）
        rm_ids = {str(hid)}
        if typ == "联系人":
            for nm in [name, raw_name]:
                if nm:
                    h = _hash_name(nm)
                    rm_ids.add(h)
                    rm_ids.add(f"wechat-bridge:FriendMessage:{h}")
        elif typ == "群聊":
            h = _hash_name(name)
            rm_ids.add(h)
            rm_ids.add(f"wechat-bridge:GroupMessage:{h}")
        wl = [x for x in ps.get("id_whitelist", []) if str(x) not in rm_ids]
        ps["id_whitelist"] = wl
        _write_config(cfg)
        # 同步 wechat-bot .env 白名单 (删备注名+微信名) + 重启 wechat-bot
        if typ == "联系人":
            names = [n for n in _read_env_names() if n != name and n != raw_name]
            _update_wechat_bot_env(names, _read_env_rooms())
            _restart_wechat_bot_async()
            yield event.plain_result(f"✅ 已移除联系人『{name}』({hid}) 从聊天白名单，正在重启 wechat-bot 生效...")
        elif typ == "群聊":
            rooms = [r for r in _read_env_rooms() if r != name]
            _update_wechat_bot_env(_read_env_names(), rooms)
            _restart_wechat_bot_async()
            yield event.plain_result(f"✅ 已移除群聊『{name}』({hid}) 从聊天白名单，正在重启 wechat-bot 生效...")
        else:
            _restart_astrbot_async()
            yield event.plain_result(f"✅ 已移除 {typ}『{name}』({hid}) 从聊天白名单，正在自动重启生效...")

    @filter.command("管理员设置")
    @filter.permission_type(filter.PermissionType.ADMIN)
    async def admin_set(self, event: AstrMessageEvent, arg1: str = ""):
        """把联系人设为管理员(可访问电脑)【仅超级管理员可操作】"""
        # 超级管理员列表为空时, 回退到现有管理员(admins_id)可操作
        sender = str(event.get_sender_id())
        cfg_now = _read_config()
        supers = [str(x) for x in cfg_now.get("super_admins_id", [])]
        admins_now = [str(x) for x in cfg_now.get("admins_id", []) if str(x) != "astrbot"]
        if supers and sender not in supers:
            yield event.plain_result("❌ 只有超级管理员才能设置管理员。")
            return
        if not supers and sender not in admins_now:
            yield event.plain_result("❌ 只有管理员才能设置管理员。")
            return
        if not arg1:
            yield event.plain_result("用法: /管理员设置 <联系人名>")
            return
        keyword = arg1
        data = _fetch_contacts()
        if data.get("error"):
            yield event.plain_result(f"⚠️ 无法连接微信 bot: {data['error']}")
            return
        typ, name, hid, raw_name = _find_target(data.get("contacts", []), data.get("rooms", []), keyword)
        if not hid or typ != "联系人":
            yield event.plain_result(f"❌ 找不到联系人 '{keyword}'。（只有个人联系人能设为管理员）")
            return
        cfg = _read_config()
        admins = [str(x) for x in cfg.get("admins_id", [])]
        if str(hid) not in admins:
            admins.append(str(hid))
            cfg["admins_id"] = admins
            _write_config(cfg)
            # 同步加入桥接白名单(备注名+微信名)，确保管理员的私聊消息能到达 AstrBot
            try:
                names = list(dict.fromkeys(_read_env_names() + [name, raw_name]))
                _update_wechat_bot_env(names, _read_env_rooms())
                _restart_wechat_bot_async()
                yield event.plain_result(f"🛡️ 已将 {name} 设为管理员（可访问电脑完整权限），已同步加入桥接白名单，正在重启生效...")
            except Exception as e:
                logger.warning(f"同步桥接白名单失败: {e}")
                _restart_astrbot_async()
                yield event.plain_result(f"🛡️ 已将 {name} 设为管理员（可访问电脑完整权限），桥接同步失败(可手动加)，正在自动重启生效...")
        else:
            yield event.plain_result(f"ℹ️ {name} 已是管理员。")

    @filter.command("管理员移除")
    @filter.permission_type(filter.PermissionType.ADMIN)
    async def admin_remove(self, event: AstrMessageEvent, arg1: str = ""):
        """移除管理员【仅超级管理员可操作】"""
        # 超级管理员列表为空时, 回退到现有管理员(admins_id)可操作
        sender = str(event.get_sender_id())
        cfg_now = _read_config()
        supers = [str(x) for x in cfg_now.get("super_admins_id", [])]
        admins_now = [str(x) for x in cfg_now.get("admins_id", []) if str(x) != "astrbot"]
        if supers and sender not in supers:
            yield event.plain_result("❌ 只有超级管理员才能移除管理员。")
            return
        if not supers and sender not in admins_now:
            yield event.plain_result("❌ 只有管理员才能移除管理员。")
            return
        if not arg1:
            yield event.plain_result("用法: /管理员移除 <联系人名>")
            return
        keyword = arg1
        data = _fetch_contacts()
        if data.get("error"):
            yield event.plain_result(f"⚠️ 无法连接微信 bot: {data['error']}")
            return
        typ, name, hid, raw_name = _find_target(data.get("contacts", []), data.get("rooms", []), keyword)
        if not hid:
            yield event.plain_result(f"❌ 找不到 '{keyword}'。")
            return
        cfg = _read_config()
        admins = [x for x in cfg.get("admins_id", []) if str(x) != str(hid)]
        cfg["admins_id"] = admins
        _write_config(cfg)
        _restart_astrbot_async()
        yield event.plain_result(f"✅ 已移除 {name} 的管理员权限，正在自动重启生效...")


# ===== 白名单管理器 Web UI (通过 AstrBot WebUI 访问) =====
_UI_HTML = """<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>微信AI - 白名单管理器</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:'Microsoft YaHei',sans-serif;background:#f5f7fa;padding:20px;color:#333}
.container{max-width:900px;margin:0 auto}
h1{font-size:22px;margin-bottom:6px}
.sub{color:#888;font-size:13px;margin-bottom:20px}
.card{background:#fff;border-radius:10px;padding:16px;margin-bottom:16px;box-shadow:0 1px 3px rgba(0,0,0,.08)}
.card h2{font-size:16px;margin-bottom:10px}
.badge{background:#e8f0fe;color:#1a73e8;border-radius:20px;padding:2px 10px;font-size:12px}
.badge-admin{background:#fdecea;color:#d93025}
.item{display:flex;align-items:center;padding:8px 10px;border-radius:8px}
.item:hover{background:#f0f4ff}
.item label{flex:1;display:flex;align-items:center;gap:10px;cursor:pointer}
.item input[type=checkbox]{width:17px;height:17px}
.item .name{font-weight:500}
.item .id{color:#999;font-size:12px}
.item .tag{font-size:11px;color:#fff;border-radius:4px;padding:1px 6px;margin-left:6px}
.tag-group{background:#1a73e8}
.item .admin-toggle{margin-left:auto;font-size:12px;color:#d93025;cursor:pointer}
.search{width:100%;padding:10px 14px;border:1px solid #ddd;border-radius:8px;margin-bottom:10px;font-size:14px}
.btn{background:#1a73e8;color:#fff;border:none;padding:12px 24px;border-radius:8px;font-size:15px;cursor:pointer}
.btn:hover{background:#1765cc}
.notice{background:#fef7e0;border:1px solid #f9e2a1;color:#8a6d1a;padding:12px;border-radius:8px;margin-bottom:16px;font-size:13px}
.hidden{display:none}
#result{margin-top:16px;padding:12px;border-radius:8px;font-size:14px}
#result.ok{background:#e6f4ea;color:#188038}
#result.err{background:#fce8e6;color:#d93025}
.section-title{margin:14px 0 8px;font-size:14px;color:#555;font-weight:600}
.empty{color:#999;font-size:13px;padding:10px;text-align:center}
</style>
</head>
<body>
<div class="container">
<h1>📇 微信AI 白名单管理器</h1>
<div class="sub">选择谁可以和 AI 聊天，谁拥有访问电脑的完整权限（管理员）</div>
<div class="notice hidden" id="loginNotice">⚠️ 未检测到微信登录。请先启动 wechat-bot 并扫码登录，然后刷新本页。</div>
<div class="card">
<h2>💬 聊天白名单 <span class="badge" id="chatBadge">全部可聊</span></h2>
<input class="search" id="search" placeholder="🔍 搜索联系人/群名...">
<div class="section-title">👥 联系人</div>
<div id="contactList"><div class="empty">加载中...</div></div>
<div class="section-title">👥 群聊</div>
<div id="roomList"><div class="empty">加载中...</div></div>
</div>
<div class="card">
<h2>🛡️ 管理员（可访问电脑） <span class="badge badge-admin" id="adminBadge">未设置</span></h2>
<div id="adminList"><div class="empty">加载中...</div></div>
</div>
<button class="btn" id="saveBtn" onclick="save()">💾 保存白名单</button>
<div id="result"></div>
</div>
<script>
const API = '/api/plug/whitelist_manager';
let contacts=[], rooms=[], chatIds=new Set(), adminIds=new Set();
async function api(p,opts){
  const sdk = window.AstrBotPluginPage;
  if (sdk && typeof sdk.apiGet === 'function') {
    try {
      let res;
      if (opts && opts.method === 'POST') { res = await sdk.apiPost(API+p, JSON.parse(opts.body||'{}')); }
      else { res = await sdk.apiGet(API+p); }
      if (res && typeof res === 'object' && 'data' in res) return res.data;
      return res;
    } catch(e) { console.error('SDK fail:', e); }
  }
  const r = await fetch(API+p, opts); return r.json();
}
function esc(s){return String(s||'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]))}
async function load(){
  const wl=await api('/whitelist');
  chatIds=new Set((wl.chatIds||[]).map(String));
  adminIds=new Set((wl.adminIds||[]).map(String));
  document.getElementById('chatBadge').textContent=wl.enabled?chatIds.size+' 个':'全部可聊';
  const wc=await api('/contacts');
  contacts=wc.contacts||[]; rooms=wc.rooms||[];
  if(wc.error||(!contacts.length&&!rooms.length))document.getElementById('loginNotice').classList.remove('hidden');
  else document.getElementById('loginNotice').classList.add('hidden');
  render();
}
function render(){
  const kw=document.getElementById('search').value.trim().toLowerCase();
  const MAX_SHOW = 100;  // 限制渲染数量, 避免 860 个联系人卡死
  const cf=(contacts.filter(c=>!kw||(c.name||'').toLowerCase().includes(kw))).slice(0,MAX_SHOW);
  const rf=(rooms.filter(r=>!kw||(r.name||'').toLowerCase().includes(kw))).slice(0,MAX_SHOW);
  const totalC=contacts.length, totalR=rooms.length;
  document.getElementById('contactList').innerHTML=cf.length?cf.map(c=>`
    <div class="item"><label><input type="checkbox" ${chatIds.has(String(c.hashId))?'checked':''} onchange="toggleChat('${c.hashId}',this.checked)">
    <span class="name">${esc(c.name)}</span><span class="id">${c.hashId}</span></label>
    <label class="admin-toggle"><input type="checkbox" ${adminIds.has(String(c.hashId))?'checked':''} onchange="toggleAdmin('${c.hashId}',this.checked)"> 🛡️管理员</label></div>`).join('')+'<div class="empty">共 '+totalC+' 个联系人, 显示前 '+MAX_SHOW+' 个(用搜索过滤)</div>':'<div class="empty">暂无联系人（需登录微信）</div>';
  document.getElementById('roomList').innerHTML=rf.length?rf.map(r=>`
    <div class="item"><label><input type="checkbox" ${chatIds.has(String(r.hashId))?'checked':''} onchange="toggleChat('${r.hashId}',this.checked)">
    <span class="name">${esc(r.name)}</span><span class="tag tag-group">群</span><span class="id">${r.hashId}</span></label></div>`).join('')+'<div class="empty">共 '+totalR+' 个群聊</div>':'<div class="empty">暂无群聊（需登录微信）</div>';
  const admins=contacts.filter(c=>adminIds.has(String(c.hashId)));
  document.getElementById('adminList').innerHTML=admins.length?admins.map(c=>`<div class="item"><span class="name">🛡️ ${esc(c.name)}</span><span class="id">${c.hashId}</span></div>`).join(''):'<div class="empty">未设置管理员</div>';
  document.getElementById('adminBadge').textContent=admins.length?admins.length+' 个':'未设置';
}
function toggleChat(id,c){c?chatIds.add(String(id)):chatIds.delete(String(id));document.getElementById('chatBadge').textContent=chatIds.size+' 个'}
function toggleAdmin(id,c){c?adminIds.add(String(id)):adminIds.delete(String(id));render()}
async function save(){
  const btn=document.getElementById('saveBtn');btn.disabled=true;
  const res=await api('/whitelist',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({chatIds:[...chatIds],adminIds:[...adminIds]})});
  const box=document.getElementById('result');
  box.className=res.status==='ok'?'ok':'err';
  box.textContent=res.status==='ok'?('✅ '+res.message):('❌ '+(res.error||'保存失败'));
  btn.disabled=false;
}
document.getElementById('search').addEventListener('input',render);
load();
</script>
</body>
</html>
"""