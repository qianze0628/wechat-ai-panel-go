// Package api 二维码: /api/qr/status + /qr.png (登录状态优先查 wechat-bot API, 二维码从日志解析)
package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/util"
)

// QrStatus 二维码状态
type QrStatus struct {
	Logged bool   `json:"logged"`
	HasQr  bool   `json:"hasQr"`
	QrURL  string `json:"qrUrl"`
}

// qrStatus 登录状态 + 二维码
// - logged: 优先调 wechat-bot /api/status 拿真实登录态 (日志里残留的 "has logged in" 不可靠,
//   重新扫码时日志仍含旧记录会导致误判为已登录)
// - hasQr/qrUrl: 从日志尾部解析最新 onScan 二维码
func qrStatus(cfg *config.Config) QrStatus {
	st := QrStatus{}
	st.Logged = wechatLoggedIn(cfg)

	candidates := []string{cfg.Logs.WechatCaptureLog, cfg.Logs.WechatStdout}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		content := util.ReadTail(p, 500*1024)
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

// wechatLoggedIn 调 wechat-bot HTTP API 获取真实登录状态
func wechatLoggedIn(cfg *config.Config) bool {
	url := "http://127.0.0.1:" + strconv.Itoa(cfg.Services.Wechat.APIPort) + "/api/status"
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var d struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if json.NewDecoder(resp.Body).Decode(&d) != nil {
		return false
	}
	return d.LoggedIn
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