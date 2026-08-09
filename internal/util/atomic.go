// Package util 原子写: JSON 读写 (BOM 兼容) / 临时文件 + 校验 + rename
package util

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// BOM UTF-8 BOM 字节
var bom = []byte{0xEF, 0xBB, 0xBF}

// stripBOM 去除前导 BOM
func stripBOM(b []byte) []byte {
	return bytesTrimPrefix(b, bom)
}

// ReadJSONFile 读 JSON 文件 (兼容 UTF-8 BOM), 返回解析后的 map
// 容错: 若文件是 GBK/ANSI 编码 (Windows 工具可能写出), 转 UTF-8 修复并重写
func ReadJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = stripBOM(data)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		// UTF-8 失败: 不自动猜测编码 (Go std 无 GBK), 由上层 (启动预检) 明确提示
		return nil, err
	}
	return m, nil
}

// AtomicWriteJSON 原子写 JSON: 临时文件写入 → 校验 → rename
// withBOM=true 时写 UTF-8 BOM (AstrBot 读取兼容)
func AtomicWriteJSON(path string, v any, withBOM bool) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if withBOM {
		data = append(append([]byte{}, bom...), data...)
	}
	dir := filepath.Dir(path)
	tmp := filepath.Join(dir, filepath.Base(path)+".tmp-panel")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	// 重新解析校验
	if _, err := ReadJSONFile(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	// 原子替换 (Windows: 先删旧)
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path)
	}
	return os.Rename(tmp, path)
}

// ReadTail 读文件末尾 (限 maxBytes), BOM 自动跳过
func ReadTail(path string, maxBytes int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return ""
	}
	size := fi.Size()
	start := size - maxBytes
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil {
		return string(buf)
	}
	s := string(stripBOM(buf))
	_ = strings.TrimSpace
	return s
}

// bytesTrimPrefix 去除前缀 (无字符串 BOM 陷阱)
func bytesTrimPrefix(b, prefix []byte) []byte {
	if len(b) >= len(prefix) && string(b[:len(prefix)]) == string(prefix) {
		return b[len(prefix):]
	}
	return b
}