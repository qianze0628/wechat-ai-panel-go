//go:build !windows

package process

// 非 Windows: 无注册表, 直接用进程 PATH (安装器一般走 brew/apt, 会改系统 PATH)。

import "os"

func registrySystemPath() string {
	return os.Getenv("PATH")
}