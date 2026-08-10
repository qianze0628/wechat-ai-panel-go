//go:build windows

package api

// 注册表 PATH 读取 (仅 Windows)。
// 分离到独立文件避免 Linux/macOS 交叉编译引入 golang.org/x/sys/windows/registry (该包无其他平台实现)。

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// refreshSystemPath 从注册表重读系统/用户 PATH, 返回合并后的 PATH 字符串。
// 修复 (2026-08-10): Go 进程 os.Environ() 是启动快照, winget/安装器修改注册表 PATH 后
// 当前进程不感知 → exec 找不到新装工具 (如 winget 装的 node/npm)。
// 每次运行命令前调用, 把注册表最新 PATH 注入子进程 env。
func refreshSystemPath() string {
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
		// 保留当前进程 PATH 中注册表未覆盖的项 (如面板自带便携工具目录)
		merged += string(os.PathListSeparator) + os.Getenv("PATH")
	}
	return merged
}