//go:build !windows

package main

// refreshSystemPathFromRegistry 非 Windows 平台: 返回空 (PATH 由包管理器直接生效)
func refreshSystemPathFromRegistry() string { return "" }
