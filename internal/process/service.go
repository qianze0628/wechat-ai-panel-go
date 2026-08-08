// Package process 服务层: AstrBot/wechat-bot/qr-server 的启动/停止/健康检查
package process

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

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
		return webui && ws && webuiHTTP, map[string]any{
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
	cmd, err := s.spawnService("astrbot", []string{exe, "run"}, s.Cfg.AstrbotRoot,
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
	serve := s.Cfg.WechatBotServe
	cmd, err := s.spawnService("wechat", []string{node, "./cli.js", "start", "-s", serve},
		s.Cfg.WechatBotDir, s.Cfg.Logs.WechatStdout, s.Cfg.Logs.WechatStderr)
	if err != nil {
		return false, fmt.Sprintf("wechat-bot 启动失败: %v", err)
	}
	return true, fmt.Sprintf("wechat-bot 已启动 (PID %d)", cmd.Process.Pid)
}

func (s *Services) startQr() (bool, string) {
	node := which("node")
	if node == "" {
		return false, "node 未安装"
	}
	if _, err := os.Stat(s.Cfg.QrServerScript); err != nil {
		return false, fmt.Sprintf("qr-server.js 不存在: %s", s.Cfg.QrServerScript)
	}
	dir := filepath.Dir(s.Cfg.QrServerScript)
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
		Args:   []string{node, s.Cfg.QrServerScript},
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

// which PATH 查找
func which(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
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
