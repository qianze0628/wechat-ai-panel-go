// Package api 监控类 API: /api/env /api/system /api/logs /api/logs/stream /api/messages
package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"wechat-ai-panel/internal/config"
	"wechat-ai-panel/internal/system"
	"wechat-ai-panel/internal/util"
)

// EnvStatus 环境检测
type EnvStatus struct {
	Node        EnvItem `json:"node"`
	Npm         EnvItem `json:"npm"`
	Uv          EnvItem `json:"uv"`
	Python      EnvItem `json:"python"`
	Astrbot     EnvItem `json:"astrbot"`
	WechatBot   EnvItem `json:"wechat_bot"`
	AstrbotRoot EnvItem `json:"astrbot_root"`
	CmdConfig   EnvItem `json:"cmd_config"`
}

type EnvItem struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
	DepsReady bool   `json:"deps_ready,omitempty"`
	Exists    bool   `json:"exists,omitempty"`
}

// detectEnv 环境检测 (与 Python detect_env 一致)
func detectEnv(cfg *config.Config) EnvStatus {
	wechatPkg := filepath.Join(cfg.WechatBotDir, "package.json")
	_, pkgErr := os.Stat(wechatPkg)
	_, nmErr := os.Stat(filepath.Join(cfg.WechatBotDir, "node_modules"))
	_, cmdErr := os.Stat(cfg.Astrbot.CmdConfig)
	_, rootErr := os.Stat(filepath.Join(cfg.AstrbotRoot, ".astrbot"))

	astrbotExe := findAstrbotExePath()
	return EnvStatus{
		Node:        EnvItem{Installed: which2("node") != "", Path: which2("node")},
		Npm:         EnvItem{Installed: which2("npm") != "", Path: which2("npm")},
		Uv:          EnvItem{Installed: which2("uv") != "", Path: which2("uv")},
		Python:      EnvItem{Installed: which2("python") != "", Path: which2("python")},
		Astrbot:     EnvItem{Installed: astrbotExe != "", Path: astrbotExe},
		WechatBot:   EnvItem{Installed: pkgErr == nil, DepsReady: nmErr == nil, Path: cfg.WechatBotDir},
		AstrbotRoot: EnvItem{Exists: rootErr == nil, Path: cfg.AstrbotRoot},
		CmdConfig:   EnvItem{Exists: cmdErr == nil, Path: cfg.Astrbot.CmdConfig},
	}
}

// findAstrbotExePath 查找 astrbot 可执行
func findAstrbotExePath() string {
	candidates := []string{
		filepath.Join(os.Getenv("USERPROFILE"), `AppData\Roaming\uv\tools\astrbot\Scripts\astrbot.exe`),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return which2("astrbot")
}

func which2(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// RegisterMonitor 注册监控路由 (env/system/logs/messages)
func (h *Handler) RegisterMonitor(cfg *config.Config) {
	h.HandleFunc("/api/env", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, detectEnv(cfg))
	})

	h.HandleFunc("/api/system", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, system.Gather())
	})

	// /api/logs?service=wechat|astrbot|qr|*_err
	h.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		svc := r.URL.Query().Get("service")
		if svc == "" {
			svc = "wechat"
		}
		path, content := logsContent(cfg, svc)
		jsonOK(w, map[string]any{"service": svc, "path": path, "content": content})
	})

	// /api/logs/stream?service=&tail= SSE
	h.HandleFunc("/api/logs/stream", func(w http.ResponseWriter, r *http.Request) {
		svc := r.URL.Query().Get("service")
		if svc == "" {
			svc = "wechat"
		}
		path := logsPath(cfg, svc)
		streamLogs(w, path)
	})

	// /api/messages?contact=&search=&limit=
	h.HandleFunc("/api/messages", func(w http.ResponseWriter, r *http.Request) {
		contact := r.URL.Query().Get("contact")
		search := r.URL.Query().Get("search")
		limit := 200
		if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		jsonOK(w, readMessages(cfg, contact, search, limit))
	})
}

// logsPath 日志路径 (含回退到 capture log)
func logsPath(cfg *config.Config, service string) string {
	switch service {
	case "astrbot":
		return cfg.Logs.AstrbotStdout
	case "astrbot_err":
		return cfg.Logs.AstrbotStderr
	case "qr":
		return cfg.Logs.QrStdout
	case "qr_err":
		return cfg.Logs.QrStderr
	case "wechat_err":
		return cfg.Logs.WechatStderr
	case "install":
		if installLogPath != "" {
			return installLogPath
		}
		return filepath.Join(panelBaseDir(), "logs", "install.log")
	case "trace":
		// AstrBot Trace 日志 (相对 cmd_config 的 trace_log_path, 基于 data 目录解析)
		if m, err := util.ReadJSONFile(cfg.Astrbot.CmdConfig); err == nil {
			if tp, ok := m["trace_log_path"].(string); ok && tp != "" {
				p := filepath.Clean(tp)
				if !filepath.IsAbs(p) {
					p = filepath.Join(cfg.AstrbotDataDir, p)
				}
				return p
			}
		}
		return filepath.Join(cfg.AstrbotDataDir, "logs", "astrbot.trace.log")
	default: // wechat
		return cfg.Logs.WechatStdout
	}
}

// logsContent 日志内容 (面板日志 + capture 回退)
func logsContent(cfg *config.Config, service string) (string, string) {
	path := logsPath(cfg, service)
	content := util.ReadTail(path, 200*1024)
	// 回退: capture log (wechat/astrbot)
	var capPath string
	if service == "wechat" {
		capPath = cfg.Logs.WechatCaptureLog
	} else if service == "astrbot" {
		capPath = cfg.Logs.AstrbotCaptureLog
	}
	if capPath != "" && capPath != path {
		if cap, err := os.ReadFile(capPath); err == nil && len(strings.TrimSpace(string(cap))) > 0 {
			if strings.TrimSpace(content) != "" {
				content = strings.TrimRight(string(cap), "\n") + "\n\n" + content
			} else {
				path = capPath
				content = string(cap)
			}
		}
	}
	return path, content
}

// streamLogs SSE 日志流: 先回放尾部, 每 2s 增量
func streamLogs(w http.ResponseWriter, path string) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// 首次: 尾部 200 行
	content := util.ReadTail(path, 200*1024)
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	// 只取最后 200 行
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	payload, _ := json.Marshal(map[string]any{"lines": lines})
	fmt.Fprintf(w, "data: %s\n\n", payload)
	fl.Flush()

	var offset int64
	if fi, err := os.Stat(path); err == nil {
		offset = fi.Size()
	}
	// 增量
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		if fi.Size() < offset {
			offset = 0 // 轮转
		}
		if fi.Size() == offset {
			continue
		}
		buf := make([]byte, fi.Size()-offset)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		_, _ = f.ReadAt(buf, offset)
		f.Close()
		offset = fi.Size()
		lines := strings.Split(string(buf), "\n")
		if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
			continue
		}
		payload, _ := json.Marshal(map[string]any{"lines": lines})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		fl.Flush()
	}
}

// readMessages 读微信消息记录 (messages.jsonl)
func readMessages(cfg *config.Config, contact, search string, limit int) map[string]any {
	path := filepath.Join(cfg.WechatBotDir, ".data", "wechat", "messages.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return map[string]any{"ok": false, "message": "消息记录不存在: " + path, "contacts": []any{}, "messages": []any{}, "total": 0}
	}
	defer f.Close()

	contacts := map[string]*struct {
		Count int  `json:"count"`
		Room  bool `json:"room"`
	}{}
	var messages []map[string]any
	scanner := newLineScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		name, _ := m["roomName"].(string)
		if name == "" {
			name, _ = m["talkerName"].(string)
		}
		text, _ := m["text"].(string)
		isRoom, _ := m["isRoom"].(bool)
		self, _ := m["self"].(bool)
		item := map[string]any{
			"timestamp": m["timestamp"], "type": m["type"], "typeName": m["typeName"],
			"isText": m["isText"], "room": isRoom, "contact": name,
			"talker": m["talkerName"], "receiver": m["receiverName"], "self": self, "text": text,
		}
		if name != "" {
			if contacts[name] == nil {
				contacts[name] = &struct {
					Count int  `json:"count"`
					Room  bool `json:"room"`
				}{Room: isRoom}
			}
			contacts[name].Count++
		}
		if contact != "" && name != contact {
			continue
		}
		if search != "" {
			hay := strings.ToLower(text + " " + name + " " + str(name))
			if !strings.Contains(hay, strings.ToLower(search)) {
				continue
			}
		}
		messages = append(messages, item)
	}
	// 排序: 最近 limit 条后按时间正序
	sortByTimestampDesc(messages)
	if len(messages) > limit {
		messages = messages[:limit]
	}
	sortByTimestampAsc(messages)

	// contacts 列表
	var contactList []map[string]any
	for n, c := range contacts {
		contactList = append(contactList, map[string]any{"name": n, "count": c.Count, "room": c.Room})
	}
	// 按 count 降序
	sortByCount(contactList)

	return map[string]any{
		"ok": true, "path": path, "total": len(messages),
		"contacts": contactList, "messages": messages,
	}
}

func str(v any) string { if s, ok := v.(string); ok { return s }; return "" }

// newLineScanner 行扫描器
func newLineScanner(f *os.File) *bufio.Scanner {
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return s
}

// sortByTimestampDesc 时间倒序 (最近在前)
func sortByTimestampDesc(items []map[string]any) {
	sort.Slice(items, func(i, j int) bool {
		return str(items[i]["timestamp"]) > str(items[j]["timestamp"])
	})
}

// sortByTimestampAsc 时间正序
func sortByTimestampAsc(items []map[string]any) {
	sort.Slice(items, func(i, j int) bool {
		return str(items[i]["timestamp"]) < str(items[j]["timestamp"])
	})
}

// sortByCount 按条数降序
func sortByCount(items []map[string]any) {
	sort.Slice(items, func(i, j int) bool {
		ci, _ := items[i]["count"].(int)
		cj, _ := items[j]["count"].(int)
		return ci > cj
	})
}