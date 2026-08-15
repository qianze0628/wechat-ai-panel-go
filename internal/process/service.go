// Package process 服务层: AstrBot/wechat-bot/qr-server 的启动/停止/健康检查
package process

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wechat-ai-panel/internal/config"
)

// Services 服务管理器
type Services struct {
	Cfg *config.Config
	mu  sync.Mutex
	cmd map[string]*exec.Cmd // 面板启动的进程句柄
}

// NewServices 创建服务管理器
func NewServices(cfg *config.Config) *Services {
	return &Services{Cfg: cfg, cmd: make(map[string]*exec.Cmd)}
}

// ServicePorts 返回服务端口列表
func (s *Services) ServicePorts(name string) []int {
	switch name {
	case "astrbot":
		return []int{s.Cfg.Services.Astrbot.WebUIPort, s.Cfg.Services.Astrbot.WSPort}
	case "wechat":
		return []int{s.Cfg.Services.Wechat.APIPort}
	case "qr":
		return []int{s.Cfg.Services.Qr.Port}
	}
	return nil
}

// HealthCheck 应用层健康检查, 返回 (ok, 详情)
func (s *Services) HealthCheck(name string) (bool, map[string]any) {
	switch name {
	case "astrbot":
		webui := PortListening(s.Cfg.Services.Astrbot.WebUIPort)
		ws := PortListening(s.Cfg.Services.Astrbot.WSPort)
		webuiHTTP := HTTPOK(fmt.Sprintf("http://127.0.0.1:%d", s.Cfg.Services.Astrbot.WebUIPort), 3*time.Second)
		// 健康 = webui HTTP 通 (dashboard) 或 ws 端口监听 (平台 up; ws 端口对 HTTP 返回 405, 只查监听)
		healthy := (webui && webuiHTTP) || ws
		return healthy, map[string]any{
			"webui_port": webui, "ws_port": ws, "webui_http": webuiHTTP,
		}
	case "wechat":
		ok := HTTPOK(fmt.Sprintf("http://127.0.0.1:%d/api/status", s.Cfg.Services.Wechat.APIPort), 3*time.Second)
		return ok, map[string]any{"api_http": ok}
	case "qr":
		ok := HTTPOK(fmt.Sprintf("http://127.0.0.1:%d/status", s.Cfg.Services.Qr.Port), 3*time.Second)
		return ok, map[string]any{"status_http": ok}
	}
	return false, map[string]any{"detail": "未知服务"}
}

// WaitHealth 等待服务健康
func (s *Services) WaitHealth(name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok, _ := s.HealthCheck(name)
		if ok {
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}

// Start 启动服务; 返回 (ok, message)
func (s *Services) Start(name string) (bool, string) {
	switch name {
	case "astrbot":
		return s.startAstrbot()
	case "wechat":
		return s.startWechat()
	case "qr":
		return s.startQr()
	}
	return false, "未知服务: " + name
}

// Stop 停止服务 (树杀 + 清理)
func (s *Services) Stop(name string) (bool, string) {
	if name != "astrbot" && name != "wechat" && name != "qr" {
		return false, "未知服务: " + name
	}
	// 按端口找 PID 并树杀
	var killed []int
	for _, port := range s.ServicePorts(name) {
		pid := GetPidOnPort(port)
		if pid > 0 {
			if err := KillPid(pid, true); err == nil {
				killed = append(killed, pid)
			}
		}
	}
	s.mu.Lock()
	if c, ok := s.cmd[name]; ok && c.Process != nil {
		c.Process.Kill()
		delete(s.cmd, name)
	}
	s.mu.Unlock()
	if len(killed) == 0 {
		return true, name + " 未在运行"
	}
	return true, fmt.Sprintf("%s 已停止 (PID %v)", name, killed)
}

// Restart 重启服务
func (s *Services) Restart(name string) (bool, string) {
	s.Stop(name)
	time.Sleep(time.Second)
	return s.Start(name)
}

// openLog 打开日志文件 (追加)
func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// spawnService 派生服务进程并记录
func (s *Services) spawnService(name string, args []string, dir string, stdout, stderr string) (*exec.Cmd, error) {
	fout, err := openLog(stdout)
	if err != nil {
		return nil, err
	}
	ferr, err := openLog(stderr)
	if err != nil {
		fout.Close()
		return nil, err
	}
	cmd, err := Spawn(SpawnOptions{
		Args:   args,
		Dir:    dir,
		Env:    BaseEnv(),
		Stdout: fout,
		Stderr: ferr,
	})
	if err != nil {
		fout.Close()
		ferr.Close()
		return nil, err
	}
	s.mu.Lock()
	s.cmd[name] = cmd
	s.mu.Unlock()
	return cmd, nil
}

func (s *Services) startAstrbot() (bool, string) {
	exe := findAstrbotExe()
	if exe == "" {
		return false, "astrbot 未安装, 请先安装"
	}
	// 预检: cmd_config.json 若是非 UTF-8 (GBK/ANSI), AstrBot 启动必崩 → 明确失败而非转圈
	if cfgPath := s.Cfg.Astrbot.CmdConfig; cfgPath != "" {
		if raw, err := os.ReadFile(cfgPath); err == nil {
			if !utf8.Valid(raw) {
				return false, "cmd_config.json 编码异常 (非 UTF-8), AstrBot 无法启动。请用面板'配置文件'页另存为 UTF-8, 或联系我们修复"
			}
		}
	}
	// 修复 (2026-08-11): astrbot_root 配置了不存在的盘符/目录时 (如 D:\.astrbot-root 但无 D 盘),
	// Spawn 会先 mkdir, 失败时回退到 astrbot_data_dir (AstrBot 会把数据写到 cmd_config 同目录,
	// dataDir 存在即可正常工作; root 仅作为工作目录)。
	workDir := s.Cfg.AstrbotRoot
	// 修复 (2026-08-12): 无 D 盘时 root/dataDir/cmd_config 目录全失败 → 最终兜底到
	// %USERPROFILE%\.astrbot / TEMP (ensureWritableDir 链), 保证 AstrBot 必然能启动。
	wd, wderr := ensureWritableDir(workDir)
	if wderr != nil {
		return false, fmt.Sprintf("AstrBot 启动失败: %v", wderr)
	}
	if wd != workDir {
		fmt.Printf("[process] astrbot_root 不可用 (%s), 回退工作目录到 %s\n", s.Cfg.AstrbotRoot, wd)
		workDir = wd
	}
	cmd, err := s.spawnService("astrbot", []string{exe, "run"}, workDir,
		s.Cfg.Logs.AstrbotStdout, s.Cfg.Logs.AstrbotStderr)
	if err != nil {
		return false, fmt.Sprintf("AstrBot 启动失败: %v", err)
	}
	return true, fmt.Sprintf("AstrBot 已启动 (PID %d)", cmd.Process.Pid)
}

func (s *Services) startWechat() (bool, string) {
	node := which("node")
	if node == "" {
		return false, "node 未安装"
	}
	pkg := filepath.Join(s.Cfg.WechatBotDir, "package.json")
	if _, err := os.Stat(pkg); err != nil {
		return false, fmt.Sprintf("wechat-bot 不存在: %s", s.Cfg.WechatBotDir)
	}
	// 确保 .env 存在 (全新 clone 没有 .env; cli.js 靠 SERVICE_TYPE 等配置启动,
	// 没有 .env 会弹交互菜单卡死)。从 .env.example 复制并写入基础配置。
	if err := ensureWechatEnv(s.Cfg.WechatBotDir, s.Cfg.WechatBotServe); err != nil {
		return false, fmt.Sprintf("wechat-bot .env 准备失败: %v", err)
	}
	serve := s.Cfg.WechatBotServe
	cmd, err := s.spawnService("wechat", []string{node, "./cli.js", "start", "-s", serve},
		s.Cfg.WechatBotDir, s.Cfg.Logs.WechatStdout, s.Cfg.Logs.WechatStderr)
	if err != nil {
		return false, fmt.Sprintf("wechat-bot 启动失败: %v", err)
	}
	return true, fmt.Sprintf("wechat-bot 已启动 (PID %d)", cmd.Process.Pid)
}

// ensureWechatEnv 确保 wechat-bot 的 .env 存在且含 SERVICE_TYPE
func ensureWechatEnv(dir, serveType string) error {
	envPath := filepath.Join(dir, ".env")
	if _, err := os.Stat(envPath); err == nil {
		return nil // 已存在
	}
	// 从 .env.example 复制
	example := filepath.Join(dir, ".env.example")
	content := ""
	if data, err := os.ReadFile(example); err == nil {
		content = string(data)
	} else {
		// 无 example: 生成最小 .env
		content = "# Generated by wechat-ai-panel\n"
	}
	// 确保 SERVICE_TYPE 存在
	if serveType != "" && !strings.Contains(content, "SERVICE_TYPE") {
		content += "\nSERVICE_TYPE='" + serveType + "'\n"
	}
	if !strings.Contains(content, "SERVICE_TYPE") {
		content += "SERVICE_TYPE='" + serveType + "'\n"
	}
	return os.WriteFile(envPath, []byte(content), 0o644)
}

func (s *Services) startQr() (bool, string) {
	node := which("node")
	if node == "" {
		return false, "node 未安装"
	}
	// qr-server 脚本定位: 优先配置路径, 回退 wechat-bot 目录
	// (全新用户 clone 后 qr-server 在 wechat-bot 里; wechat-bot 是 ESM 包, 脚本须用 .cjs 扩展)
	qrScript := s.Cfg.QrServerScript
	if _, err := os.Stat(qrScript); err != nil {
		for _, cand := range []string{
			filepath.Join(s.Cfg.WechatBotDir, "qr-server.cjs"),
			filepath.Join(s.Cfg.WechatBotDir, "qr-server.js"),
		} {
			if _, err2 := os.Stat(cand); err2 == nil {
				qrScript = cand
				break
			}
		}
	}
	if _, err := os.Stat(qrScript); err != nil {
		return false, fmt.Sprintf("qr-server 脚本不存在: %s", s.Cfg.QrServerScript)
	}
	dir := filepath.Dir(qrScript)
	// 传 WECHAT_LOG_FILE 给 qr-server (让它读正确的 wechat-bot 日志; 兼容旧版默认路径)
	env := append(BaseEnv(), "WECHAT_LOG_FILE="+s.Cfg.Logs.WechatCaptureLog)
	fout, err1 := openLog(s.Cfg.Logs.QrStdout)
	if err1 != nil {
		return false, fmt.Sprintf("qr-server 日志打开失败: %v", err1)
	}
	ferr, err2 := openLog(s.Cfg.Logs.QrStderr)
	if err2 != nil {
		fout.Close()
		return false, fmt.Sprintf("qr-server 日志打开失败: %v", err2)
	}
	cmd, err := Spawn(SpawnOptions{
		Args:   []string{node, qrScript},
		Dir:    dir,
		Env:    env,
		Stdout: fout,
		Stderr: ferr,
	})
	if err != nil {
		fout.Close()
		ferr.Close()
		return false, fmt.Sprintf("qr-server 启动失败: %v", err)
	}
	s.mu.Lock()
	s.cmd["qr"] = cmd
	s.mu.Unlock()
	return true, fmt.Sprintf("qr-server 已启动 (PID %d)", cmd.Process.Pid)
}

// findAstrbotExe 查找 astrbot 可执行文件 (uv tools → PATH)
func findAstrbotExe() string {
	candidates := []string{
		filepath.Join(os.Getenv("USERPROFILE"), `AppData\Roaming\uv\tools\astrbot\Scripts\astrbot.exe`),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return which("astrbot")
}

// which 查找可执行文件: 候选目录优先 + 注册表 PATH 刷新回退。
// 修复 (2026-08-15 对抗式审查 critical): 原 exec.LookPath 用面板进程 PATH 快照,
// 便携 node (装到 ~/.wechat-ai-panel/nodejs, 不进系统 PATH) 装完后 startWechat/startQr
// 必报 "node 未安装" / "executable file not found"。与 api 包 which2 同方案:
// ① 候选目录直接 os.Stat ② Windows 注册表 PATH 刷新后手工查 (.exe/.cmd/.bat)。
func which(name string) string {
	home, _ := os.UserHomeDir()
	ext := ".exe"
	if name == "npm" {
		ext = ".cmd" // 便携 node 无 npm.exe
	}
	// 面板自管理便携工具目录 + 用户级安装目录
	dirs := []string{
		filepath.Join(home, ".wechat-ai-panel", "nodejs"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "AppData", "Roaming", "uv", "bin"),
	}
	for _, d := range dirs {
		for _, e := range []string{ext, ".exe", ".cmd"} {
			c := filepath.Join(d, name+e)
			if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Size() > 0 {
				return c
			}
		}
	}
	// astrbot: uv tools 固定路径优先 (与 api 包一致)
	if name == "astrbot" {
		c := filepath.Join(home, `AppData\Roaming\uv\tools\astrbot\Scripts\astrbot.exe`)
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	// 注册表 PATH 刷新后查 (进程 PATH 是启动快照, 新装工具不在)
	if runtime.GOOS == "windows" {
		if p := lookupWithSystemPath(name); p != "" {
			return p
		}
	}
	// 最后: 原 LookPath (系统 PATH 场景)
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// ensureWritableDir 确保目录存在且可写; 失败时自动回退到可靠目录并返回。
// 修复 (2026-08-12): 全新电脑可能无 D 盘/配置指向不存在的盘符 (D:\.astrbot-root),
// 此时 os.MkdirAll 失败 → 必须兜底到必然可写位置, 否则 AstrBot/wechat-bot 全链路死。
// 回退顺序: ① 可靠用户目录 %USERPROFILE%\.wechat-ai-panel ② 系统 TEMP。
func ensureWritableDir(dir string) (string, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			return dir, nil
		}
	}
	// 兜底 1: 用户目录下的面板专用目录 (C 盘, 全新电脑必有)
	home, _ := os.UserHomeDir()
	for _, sub := range []string{".wechat-ai-panel", "AppData\\Local\\WeChatPanel", ".astrbot"} {
		cand := filepath.Join(home, sub)
		if err := os.MkdirAll(cand, 0o755); err == nil {
			return cand, nil
		}
	}
	// 兜底 2: 系统 TEMP
	if tmp := os.Getenv("TEMP"); tmp != "" {
		if err := os.MkdirAll(tmp, 0o755); err == nil {
			return tmp, nil
		}
	}
	return "", fmt.Errorf("无法创建任何工作目录 (原始: %s)", dir)
}

// HTTPOK 探测 HTTP 服务健康
func HTTPOK(url string, timeout time.Duration) bool {
	client := http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// (end)

// lookupWithSystemPath 用注册表最新 PATH 手工查可执行文件 (绕过进程 PATH 快照)。
// Windows: 读注册表 System/User PATH 合并, 查 .exe/.cmd/.bat; 非 Windows 直接查进程 PATH。
func lookupWithSystemPath(name string) string {
	var sysPath string
	if runtime.GOOS == "windows" {
		sysPath = registrySystemPath()
	} else {
		sysPath = os.Getenv("PATH")
	}
	if sysPath == "" {
		return ""
	}
	exts := []string{".exe", ".cmd", ".bat"}
	for _, d := range strings.Split(sysPath, string(os.PathListSeparator)) {
		if d == "" {
			continue
		}
		for _, e := range exts {
			c := filepath.Join(d, name+e)
			if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Size() > 0 {
				return c
			}
		}
	}
	return ""
}
