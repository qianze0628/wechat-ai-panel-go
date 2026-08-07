// Package api 二维码: /api/qr/status + /qr.png (从 wechat-bot 日志解析)
package api

import (
	"io"
	"net/http"
	"strings"

	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/util"
)

// QrStatus 二维码状态
type QrStatus struct {
	Logged bool   `json:"logged"`
	HasQr  bool   `json:"hasQr"`
	QrURL  string `json:"qrUrl"`
}

// qrStatus 从日志解析登录状态与二维码 URL
func qrStatus(cfg *config.Config) QrStatus {
	st := QrStatus{}
	candidates := []string{cfg.Logs.WechatCaptureLog, cfg.Logs.WechatStdout}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		content := util.ReadTail(p, 500*1024)
		if !st.Logged && (strings.Contains(content, "has logged in") || strings.Contains(content, "已登录")) {
			st.Logged = true
		}
		if !st.HasQr {
			idx := strings.LastIndex(content, "onScan: ")
			if idx >= 0 {
				rest := strings.TrimSpace(content[idx+len("onScan: "):])
				url := rest
				if sp := strings.IndexAny(rest, " \n"); sp >= 0 {
					url = rest[:sp]
				}
				if strings.HasPrefix(url, "https://") {
					st.QrURL = url
					st.HasQr = true
				}
			}
		}
	}
	return st
}

// RegisterQr 注册二维码 API
func (h *Handler) RegisterQr(cfg *config.Config) {
	h.HandleFunc("/api/qr/status", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, qrStatus(cfg))
	})
	h.HandleFunc("/qr.png", func(w http.ResponseWriter, r *http.Request) {
		st := qrStatus(cfg)
		if !st.HasQr {
			placeholderQr(w, "等待二维码...")
			return
		}
		resp, err := http.Get(st.QrURL)
		if err != nil {
			placeholderQr(w, "二维码获取失败")
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "image/png")
		io.Copy(w, resp.Body)
	})
}

// placeholderQr 占位二维码 (SVG 文本)
func placeholderQr(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "image/svg+xml")
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="320" height="320">` +
		`<rect width="320" height="320" fill="#eee"/>` +
		`<text x="160" y="160" text-anchor="middle" fill="#888" font-size="18">` + text + `</text></svg>`
	io.WriteString(w, svg)
}