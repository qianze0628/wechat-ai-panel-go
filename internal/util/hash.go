// Package util 通用工具: hashId 算法 / 公众号判断
package util

// HashName 与 wechat-bot bridge-integration.js hashId() 完全一致的 JS 式字符串哈希
// JS: h = ((h<<5) - h + charCode) | 0  (32 位有符号环绕)
// 返回 abs(h) + 10000
func HashName(s string) int64 {
	if s == "" {
		s = "unknown"
	}
	var h int32
	for _, ch := range s {
		// ((h<<5) - h + code) 用 int64 计算再截断为 int32
		next := int64(h)<<5 - int64(h) + int64(ch)
		h = int32(next)
	}
	v := int64(h)
	if v < 0 {
		v = -v
	}
	return v + 10000
}

// IsOfficialID 公众号/服务号判断: 32 位纯 hex 短 id 或系统特殊号
func IsOfficialID(rawID string) bool {
	if rawID == "" {
		return false
	}
	special := map[string]bool{
		"filehelper": true, "weixin": true, "weibo": true, "qqmail": true,
		"fmessage": true, "tmessage": true, "qmessage": true, "medianote": true,
		"floatbottle": true, "lbsapp": true, "shakeapp": true, "newsapp": true,
		"filetransferhelper": true,
	}
	if special[rawID] {
		return true
	}
	s := rawID
	if len(s) > 0 && s[0] == '@' {
		s = s[1:]
	}
	if len(s) == 32 {
		for _, c := range s {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	}
	return false
}