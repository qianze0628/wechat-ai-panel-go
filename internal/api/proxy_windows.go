//go:build windows

package api

import (
	"net/http"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func init() {
	systemProxyFor = windowsSystemProxy
}

// windowsSystemProxy 读取注册表 IE/系统代理设置.
// 键: HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings
//   ProxyEnable=1 且 ProxyServer="host:port" 或 "http=host:port;https=host:port"
// 代理脚本 (AutoConfigURL PAC) 不做解析, 返回空 (保持直连, 避免复杂 PAC 引擎).
func windowsSystemProxy(req *http.Request) string {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled != 1 {
		return ""
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil || server == "" {
		return ""
	}
	// 表单 1: "host:port" — 单代理, 全协议通用
	if !strings.Contains(server, "=") {
		return server
	}
	// 表单 2: "http=host:port;https=host:port" — 按协议选
	scheme := "http"
	if req.URL != nil && req.URL.Scheme == "https" {
		scheme = "https"
	}
	for _, part := range strings.Split(server, ";") {
		p := strings.TrimSpace(part)
		if strings.HasPrefix(p, scheme+"=") {
			return strings.TrimPrefix(p, scheme+"=")
		}
	}
	return ""
}
