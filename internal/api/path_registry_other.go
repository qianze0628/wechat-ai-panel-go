//go:build !windows

package api

// 非 Windows 平台: 无注册表, 直接用进程 PATH (安装器一般走 brew/apt, 会改系统 PATH 对当前进程可能也不可见,
// 但非 Windows 场景较少, 保持简单)。

import "os"

func refreshSystemPath() string {
	return os.Getenv("PATH")
}