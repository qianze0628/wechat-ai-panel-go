//go:build windows

package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// refreshSystemPathFromRegistry 从注册表重读系统/用户 PATH, 返回合并后的 PATH 字符串。
// 修复 (2026-08-15 v0.6.2): Go 进程 os.Environ() 是启动快照, 面板装完便携工具后
// (node/uv 等) 当前进程不感知注册表 PATH 变化 → exec.LookPath 检测不到新装工具。
func refreshSystemPathFromRegistry() string {
	var paths []string
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetStringValue("Path"); err == nil {
			paths = append(paths, v)
		}
		k.Close()
	}
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
