// Package api HTTP 下载代理支持: 环境变量代理 + Windows 系统代理 (注册表).
// 修复 (2026-08-15): 用户挂代理 (如 Clash 系统代理) 时 Go 默认 http.Client 只认
// HTTP_PROXY/HTTPS_PROXY 环境变量, 不读 Windows 注册表代理设置 → "挂代理也不生效".
// proxyFunc 融合两者: 环境变量代理优先, 其次读注册表 IE/系统代理 (ProxyEnable/ProxyServer).

package api

import (
	"net/http"
	"net/url"
	"time"
)

// proxyFunc 返回 Transport.Proxy: 环境变量代理优先 → Windows 注册表系统代理兜底.
func proxyFunc() func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		// 1. 环境变量代理 (HTTP_PROXY/HTTPS_PROXY/NO_PROXY) — 标准 Go 行为
		if u, err := http.ProxyFromEnvironment(req); err == nil && u != nil {
			return u, nil
		}
		// 2. Windows 注册表系统代理 (仅 HTTP/HTTPS 目标)
		if sysProxy := systemProxyFor(req); sysProxy != "" {
			pu, err := url.Parse(sysProxy)
			if err == nil {
				return pu, nil
			}
		}
		return nil, nil
	}
}

// systemProxyFor 读取系统/IE 代理设置 (Windows 注册表; 其他平台返回 "").
// 在 proxy_windows.go / proxy_other.go 分别实现.
var systemProxyFor func(req *http.Request) string

// newDownloadClient 构建带代理感知的下载客户端 (302 跟随 + 超时 + 环境/系统代理).
// 修复 (2026-08-15): 用户挂代理 (Clash 系统代理) 时默认 http.Client 不读注册表 →
// 挂代理也不生效; 本客户端融合环境变量代理与 Windows 系统代理, 且支持仓库/工具下载.
func newDownloadClient() *http.Client {
	return &http.Client{
		Timeout:   8 * time.Minute,
		Transport: &http.Transport{Proxy: proxyFunc()},
	}
}
