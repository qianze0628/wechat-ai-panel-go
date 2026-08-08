# ===== AstrBot 群聊 ICL 污染过滤补丁 (面板自动检测/重打) =====
# 由 wechat-ai-panel 内置分发, AstrBot 升级后若丢失会自动重新打上。
# 检测标记: 文件中包含 "_is_ai_related" 即视为已打补丁。
#
# === 需要插入的内容(按 AstrBot 4.27.x 结构) ===
#
# 1) 在 "from astrbot.api.platform import MessageType" 之后插入:
#
# def _is_ai_related(event) -> bool:
#     # 机器人自己发的内容 (回复/分析) 保留
#     if str(event.message_obj.sender.qq) == str(event.get_self_id()):
#         return True
#     # @机器人 / 引用消息保留
#     for comp in event.get_messages():
#         if isinstance(comp, At) and str(comp.qq) in (str(event.get_self_id()), 'all'):
#             return True
#         if isinstance(comp, Reply):
#             return True
#     # 群指令词 (如 /白名单) 保留
#     text = ''.join(getattr(c, 'text', '') or '' for c in event.get_messages())
#     if text.strip().startswith('/'):
#         return True
#     return False
#
# 2) 在 handle_message 入口 (GROUP_MESSAGE 检查后) 插入过滤:
#
#         # 过滤: 只记录 @机器人 / 引用机器人 / 机器人自己回复 / 群指令, 纯闲聊不进上下文
#         if not _is_ai_related(event):
#             return
#
# 注意: 此文件本身只是补丁说明; 实际插入逻辑在 patch.go 中, 按
# 锚点匹配 (import 行 / handle_message 入口), 与版本差异兼容。