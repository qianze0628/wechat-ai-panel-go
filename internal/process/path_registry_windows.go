//go:build windows

package process

// 注册表 PATH 读取 (仅 Windows)。
// 与 api 包 path_registry_windows.go 同实现, 供 which() 的 lookupWithSystemPath 使用。

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// registrySystemPath 从注册表重读系统/用户 PATH, 返回合并后的 PATH 字符串。
// 修复 (2026-08-15 对抗式审查 critical): 服务启动链路 which(node) 原用 exec.LookPath
// (进程 PATH 快照) → 便携 node 装到 ~/.wechat-ai-panel/nodejs (不进系统 PATH) 后启动服务必失败。
// 注册表 PATH 是"最新真相", 进程 PATH 是启动快照, 必须从注册表重读。
func registrySystemPath() string {
	var paths []string
	// 系统 PATH (HKLM)
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetStringValue("Path"); err == nil {
			paths = append(paths, v)
		}
		k.Close()
	}
	// 用户 PATH (HKCU)
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetStringValue("Path"); err == nil {
			paths = append(paths, v)
		}
		k.Close()
	}
	merged := strings.Join(paths, string(os.PathListSeparator))
	if merged != "" {
		merged += string(os.PathListSeparator) + os.Getenv("PATH")
	}
	return merged
}