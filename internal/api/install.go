// Package api 安装引擎: /api/install (多平台) + /api/install/status
package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"wechat-ai-panel/internal/config"
)

// StageState 单个安装阶段的状态 (结构化进度, 供前端阶段卡片展示)
type StageState struct {
	ID     string `json:"id"`     // "env" | "clone" | "npm" | "astrbot" | "verify"
	Label  string `json:"label"`  // "环境工具链"
	Status string `json:"status"` // "pending" | "running" | "done" | "error" | "manual"
	Detail string `json:"detail"` // 人类可读说明 (如 "检测到 Node.js 未安装")
	Error  string `json:"error"`  // 失败原因 (可读中文)
}

// InstallState 安装状态 (单例)
type InstallState struct {
	mu       sync.Mutex
	Running  bool     `json:"running"`
	Logs     []string `json:"logs"`
	Done     bool     `json:"done"`
	OK       *bool    `json:"ok"`
	Platform string   `json:"platform"`
	Where    map[string]any `json:"install_where"`
	// 结构化进度 (v0.1.6+)
	Stage      string `json:"stage"`       // 当前阶段 id
	StageLabel string `json:"stage_label"` // 当前阶段名
	Overall    int    `json:"overall"`     // 总进度 0-100
	Stages     []StageState `json:"stages"` // 各阶段状态
	NeedManual bool   `json:"need_manual"` // 是否等待用户手动操作
	ManualHint string `json:"manual_hint"` // 手动操作指引 (中文)
}

var installState = &InstallState{OK: nil, Stages: []StageState{}}

// stageIDs 阶段顺序与百分比权重 (参考 AstrBot UpdateProgress 的加权模型)
var stagePlan = []struct {
	id     string
	label  string
	weight int // 该阶段占总进度的百分比
}{
	{"env", "环境工具链", 20},
	{"clone", "拉取 wechat-bot 源码", 15},
	{"npm", "安装 wechat-bot 依赖 (npm install)", 35},
	{"astrbot", "安装 AstrBot", 25},
	{"verify", "验证", 5},
}

// detectPlatform 检测当前平台
func detectPlatform() string {
	if runtime.GOOS == "windows" {
		return "windows"
	}
	if runtime.GOOS == "darwin" {
		return "mac"
	}
	return "linux"
}

// planInstallTasks 规划安装任务 (wechatRepo 非空时, 缺源码自动 git clone 优化版)
// 分阶段: 1) 环境工具链 (node/npm/uv/python) 2) wechat-bot 源码+依赖 3) AstrBot
func planInstallTasks(platform, wechatDir, astrbotRoot, wechatRepo string) []map[string]string {
	var tasks []map[string]string

	// ---- 阶段 1: 环境工具链 (缺失时自动安装) ----
	// node (含 npm): wechat-bot 运行依赖
	if which2("node") == "" {
		tasks = append(tasks, map[string]string{
			"label": envInstallLabel(platform, "node"),
			"kind":  "env_node", "target": "",
		})
	} else if which2("npm") == "" {
		// 有 node 无 npm (罕见): 单独提示装 npm
		tasks = append(tasks, map[string]string{
			"label": "npm 未找到, 请安装 Node.js (含 npm)", "kind": "warn", "target": "",
		})
	}
	// uv: AstrBot 安装器 (Python 包管理)
	if which2("uv") == "" {
		tasks = append(tasks, map[string]string{
			"label": envInstallLabel(platform, "uv"),
			"kind":  "env_uv", "target": "",
		})
	}
	// python3: AstrBot 运行需要 (uv tool 会自动带, 但保险起见检测)
	if which2("python") == "" && which2("python3") == "" {
		tasks = append(tasks, map[string]string{
			"label": envInstallLabel(platform, "python"),
			"kind":  "env_python", "target": "",
		})
	}

	// ---- 阶段 2: wechat-bot 源码 + 依赖 ----
	pkg := filepath.Join(wechatDir, "package.json")
	nm := filepath.Join(wechatDir, "node_modules")
	if _, err := os.Stat(pkg); err == nil {
		if _, err := os.Stat(nm); err != nil {
			tasks = append(tasks, map[string]string{
				"label": "npm install (wechat-bot @ " + wechatDir + ")",
				"kind":  "npm", "target": wechatDir,
			})
		}
	} else {
		if wechatRepo != "" {
			tasks = append(tasks, map[string]string{
				"label": "git clone " + wechatRepo + " → " + wechatDir,
				"kind":  "clone", "target": wechatDir, "repo": wechatRepo,
			})
		} else {
			tasks = append(tasks, map[string]string{
				"label": "wechat-bot 源码缺失: " + wechatDir, "kind": "warn", "target": wechatDir,
			})
		}
	}

	// ---- 阶段 3: AstrBot ----
	if which2("astrbot") == "" {
		tasks = append(tasks, map[string]string{
			"label": "uv tool install astrbot", "kind": "uv", "target": astrbotRoot,
		})
	}
	return tasks
}

// envInstallCmd 返回安装环境依赖的实际命令 (平台感知; 尽力而为, 失败由日志提示用户手动装)
func envInstallCmd(platform, name string) *exec.Cmd {
	switch name {
	case "node":
		switch platform {
		case "windows":
			// winget 是 Win10/11 自带; 失败会进入日志, 用户可手动到 nodejs.org 下载
			return exec.Command("winget", "install", "--id", "OpenJS.NodeJS.LTS", "--accept-source-agreements", "--accept-package-agreements", "--silent")
		case "mac":
			return exec.Command("brew", "install", "node")
		default: // linux
			return exec.Command("bash", "-c", "curl -fsSL https://deb.nodesource.com/setup_20.x | sudo bash - && sudo apt-get install -y nodejs")
		}
	case "uv":
		// uv 官方安装
		if platform == "windows" {
			// Windows: 用 PowerShell 下载 uv.exe 到 %USERPROFILE%\.local\bin
			// (避免依赖 bash/WSL; 装完后续命令需刷新 PATH)
			return exec.Command("powershell", "-NoProfile", "-Command",
				"$env:UV_DIR = Join-Path $env:USERPROFILE '.local\\bin'; "+
					"New-Item -ItemType Directory -Force -Path $env:UV_DIR | Out-Null; "+
					"$exe = Join-Path $env:UV_DIR 'uv.exe'; "+
					"Invoke-WebRequest -Uri 'https://astral.sh/uv/0.5.x/installer.exe' -OutFile (Join-Path $env:TEMP 'uv-installer.exe') -UseBasicParsing; "+
					"$i = Start-Process -FilePath (Join-Path $env:TEMP 'uv-installer.exe') -ArgumentList 'install --no-modify-path --target', $env:UV_DIR -Wait -PassThru; "+
					"exit $i.ExitCode")
		}
		return exec.Command("bash", "-c", "curl -LsSf https://astral.sh/uv/install.sh | sh")
	case "python":
		switch platform {
		case "windows":
			return exec.Command("winget", "install", "--id", "Python.Python.3.12", "--accept-source-agreements", "--accept-package-agreements", "--silent")
		case "mac":
			return exec.Command("brew", "install", "python@3.12")
		default:
			return exec.Command("bash", "-c", "sudo apt-get install -y python3 python3-pip")
		}
	}
	return nil
}

// envInstallLabel 返回环境依赖的安装提示 (平台感知)
func envInstallLabel(platform, name string) string {
	switch name {
	case "node":
		switch platform {
		case "windows":
			return "安装 Node.js: winget install OpenJS.NodeJS.LTS (或到 nodejs.org 下载)"
		case "mac":
			return "安装 Node.js: brew install node"
		default:
			return "安装 Node.js: curl -fsSL https://deb.nodesource.com/setup_20.x | sudo bash - && sudo apt-get install -y nodejs"
		}
	case "uv":
		return "安装 uv: curl -LsSf https://astral.sh/uv/install.sh | sh (Windows: pip install uv 或官网安装包)"
	case "python":
		switch platform {
		case "windows":
			return "安装 Python: winget install Python.Python.3.12 (或到 python.org 下载)"
		case "mac":
			return "安装 Python: brew install python@3.12"
		default:
			return "安装 Python: sudo apt-get install -y python3 python3-pip"
		}
	}
	return "安装 " + name
}

// runInstall 后台执行安装 (分阶段执行器: 环境 → clone → npm → astrbot → 验证)
// 每阶段有结构化状态 (Stages), 总进度 Overall 按阶段权重推进 (参考 AstrBot UpdateProgress)
func runInstall(tasks []map[string]string, platform, wechatDir, astrbotRoot string) {
	installState.mu.Lock()
	ok := true
	installState.Running = true
	installState.Done = false
	installState.Platform = platform
	installState.Logs = nil
	installState.NeedManual = false
	installState.ManualHint = ""
	installState.Overall = 0
	installState.Stage = ""
	installState.StageLabel = ""
	installState.Stages = make([]StageState, 0, len(stagePlan))
	for _, sp := range stagePlan {
		installState.Stages = append(installState.Stages, StageState{ID: sp.id, Label: sp.label, Status: "pending"})
	}
	installState.Where = map[string]any{
		"platform": platform, "wechat_dir": wechatDir, "astrbot_dir": astrbotRoot,
	}
	installState.mu.Unlock()

	// PATH 累计: 每装完一个工具把安装目录加进 PATH, 供后续命令使用 (修复"装完 node 找不到 npm")
	extraPaths := []string{}

	// 阶段推进辅助
	setStage := func(id, label string, weightBase int) {
		installState.mu.Lock()
		installState.Stage = id
		installState.StageLabel = label
		for i := range installState.Stages {
			if installState.Stages[i].ID == id {
				installState.Stages[i].Status = "running"
			}
		}
		installState.mu.Unlock()
	}
	stageDone := func(id string, detail string, errMsg string) {
		installState.mu.Lock()
		overall := 0
		for i := range installState.Stages {
			if installState.Stages[i].ID == id {
				if errMsg != "" {
					installState.Stages[i].Status = "error"
					installState.Stages[i].Error = errMsg
				} else {
					installState.Stages[i].Status = "done"
				}
				installState.Stages[i].Detail = detail
			}
			if installState.Stages[i].Status == "done" {
				overall += stagePlan[i].weight
			}
		}
		installState.Overall = overall
		installState.mu.Unlock()
	}
	addLog := func(line string) {
		installState.mu.Lock()
		installState.Logs = append(installState.Logs, line)
		installState.mu.Unlock()
	}

	// 运行命令并捕获输出; 返回 (ok, errMsg)
	// 注意: 不能用 cmd.Run() + StdoutPipe 组合 —— npm install 的 prepare 钩子 (husky/npx)
	// 会派生孙进程继承管道, Run() 会等孙进程退出导致卡死。用文件重定向最可靠。
	runCmd := func(cmd *exec.Cmd, timeout time.Duration) (bool, string) {
		if cmd == nil {
			return false, "命令为空"
		}
		env := append(os.Environ(), extraPaths...)
		env = append(env, "PYTHONIOENCODING=utf-8")
		cmd.Env = env
		// stdout/stderr → 临时文件, 后台 goroutine 实时读进 Logs
		tmpOut, err1 := os.CreateTemp("", "install-*.out")
		tmpErr, err2 := os.CreateTemp("", "install-*.err")
		if err1 != nil || err2 != nil {
			return false, "创建临时日志文件失败"
		}
		defer os.Remove(tmpOut.Name())
		defer os.Remove(tmpErr.Name())
		cmd.Stdout = tmpOut
		cmd.Stderr = tmpErr
		if err := cmd.Start(); err != nil {
			return false, err.Error()
		}
		// 实时读文件 (每 300ms 增量读)
		stopTail := make(chan struct{})
		go tailLogFile(tmpOut.Name(), stopTail)
		go tailLogFile(tmpErr.Name(), stopTail)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var runErr error
		select {
		case err := <-done:
			runErr = err
		case <-time.After(timeout):
			cmd.Process.Kill()
			runErr = fmt.Errorf("执行超时 (已强制结束)")
		}
		close(stopTail)
		// 读完残留
		tailLogFileSync(tmpOut.Name())
		tailLogFileSync(tmpErr.Name())
		if runErr != nil {
			return false, runErr.Error()
		}
		return true, ""
	}

	// ===== 阶段 1: 环境工具链 =====
	setStage("env", "环境工具链", 0)
	addLog("[env] 检测环境工具链 (node/npm/uv/python/git)...")
	envOK := true
	for _, t := range tasks {
		if t["kind"] == "env_node" || t["kind"] == "env_uv" || t["kind"] == "env_python" {
			addLog("[env] [start] " + t["label"])
			cmd := envInstallCmd(platform, strings.TrimPrefix(t["kind"], "env_"))
			ok2, errMsg := runCmd(cmd, 300*time.Second)
			if ok2 {
				addLog("[env] [done] " + t["label"] + " exit=0")
				// 安装目录加入 PATH (Windows: %USERPROFILE%\.local\bin; 标准 node 目录)
				home, _ := os.UserHomeDir()
				extraPaths = append(extraPaths,
					"PATH="+os.Getenv("PATH")+string(os.PathListSeparator)+filepath.Join(home, ".local", "bin"),
				)
			} else {
				envOK = false
				addLog("[env] [error] " + t["label"] + " FAILED: " + errMsg)
				installState.mu.Lock()
				installState.NeedManual = true
				installState.ManualHint = toolManualHint(platform, strings.TrimPrefix(t["kind"], "env_"))
				installState.mu.Unlock()
			}
		}
	}
	// 重新检测 (PATH 刷新后)
	if which2("node") == "" {
		envOK = false
	}
	if !envOK {
		stageDone("env", "环境工具链未就绪", "部分工具安装失败, 请按提示手动安装后重试")
		finishInstall(false)
		return
	}
	stageDone("env", "环境工具链就绪", "")
	addLog("[env] [done] 环境工具链就绪")

	// ===== 阶段 2: clone wechat-bot 源码 =====
	setStage("clone", "拉取 wechat-bot 源码", 0)
	cloneTask := findTask(tasks, "clone")
	if cloneTask != nil {
		addLog("[clone] [start] " + cloneTask["label"])
		_ = os.MkdirAll(wechatDir, 0o755)
		cmd := exec.Command("git", "clone", "--depth", "1", cloneTask["repo"], wechatDir)
		ok2, errMsg := runCmd(cmd, 600*time.Second)
		if !ok2 {
			stageDone("clone", "git clone 失败", errMsg)
			installState.mu.Lock()
			installState.NeedManual = true
			installState.ManualHint = "git clone 失败: " + errMsg + "\n可手动执行: git clone --depth 1 " + cloneTask["repo"] + " " + wechatDir
			installState.mu.Unlock()
			finishInstall(false)
			return
		}
		addLog("[clone] [done] 源码已拉取")
	}
	stageDone("clone", "源码就绪", "")

	// ===== 阶段 3: npm install =====
	setStage("npm", "安装 wechat-bot 依赖", 0)
	pkgExists := fileExists(filepath.Join(wechatDir, "package.json"))
	nmExists := fileExists(filepath.Join(wechatDir, "node_modules"))
	if pkgExists && !nmExists {
		addLog("[npm] [start] npm install (wechat-bot)")
		cmd := exec.Command("npm", "install")
		cmd.Dir = wechatDir
		ok2, errMsg := runCmd(cmd, 600*time.Second)
		if !ok2 {
			stageDone("npm", "npm install 失败", errMsg)
			installState.mu.Lock()
			installState.NeedManual = true
			installState.ManualHint = "npm install 失败: " + errMsg + "\n可手动执行: cd " + wechatDir + " && npm install"
			installState.mu.Unlock()
			finishInstall(false)
			return
		}
		addLog("[npm] [done] npm install 完成")
	} else if !pkgExists {
		addLog("[npm] [warn] wechat-bot 源码缺失 (package.json 不存在)")
	}
	stageDone("npm", "依赖就绪", "")

	// ===== 阶段 4: 安装 AstrBot =====
	setStage("astrbot", "安装 AstrBot", 0)
	if which2("astrbot") == "" {
		addLog("[astrbot] [start] uv tool install astrbot")
		cmd := exec.Command("uv", "tool", "install", "astrbot")
		ok2, errMsg := runCmd(cmd, 900*time.Second)
		if !ok2 {
			stageDone("astrbot", "AstrBot 安装失败", errMsg)
			installState.mu.Lock()
			installState.NeedManual = true
			installState.ManualHint = "AstrBot 安装失败: " + errMsg + "\n可手动执行: uv tool install astrbot"
			installState.mu.Unlock()
			finishInstall(false)
			return
		}
		addLog("[astrbot] [done] AstrBot 已安装")
	}
	stageDone("astrbot", "AstrBot 就绪", "")

	// ===== 阶段 5: 验证 =====
	setStage("verify", "验证", 0)
	nodeV, _ := exec.LookPath("node")
	uvPath, _ := exec.LookPath("uv")
	astrbotPath, _ := exec.LookPath("astrbot")
	detail := "node=" + ternary(nodeV != "", "✓", "✗") +
		" uv=" + ternary(uvPath != "", "✓", "✗") +
		" astrbot=" + ternary(astrbotPath != "", "✓", "✗")
	if nodeV != "" && uvPath != "" && astrbotPath != "" {
		addLog("[verify] [done] " + detail)
		stageDone("verify", detail, "")
		ok = true
	} else {
		addLog("[verify] [error] " + detail)
		stageDone("verify", detail, "部分组件验证未通过")
		ok = false
	}

	finishInstall(ok)
}

// finishInstall 收尾: 置 Running/Done/OK
func finishInstall(ok bool) {
	installState.mu.Lock()
	installState.Running = false
	installState.Done = true
	installState.OK = &ok
	installState.mu.Unlock()
}

// findTask 在任务列表里找指定 kind 的任务
func findTask(tasks []map[string]string, kind string) map[string]string {
	for _, t := range tasks {
		if t["kind"] == kind {
			return t
		}
	}
	return nil
}

// fileExists 判断文件/目录是否存在
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ternary Go 没有三元; 小工具
func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// toolManualHint 手动安装指引 (中文可读)
func toolManualHint(platform, name string) string {
	switch name {
	case "node":
		if platform == "windows" {
			return "Node.js 未安装或安装失败。请到 https://nodejs.org 下载 LTS 版安装 (安装时勾选 'Add to PATH'), 完成后点\"重新检测\""
		}
		return "Node.js 未安装。请执行: curl -fsSL https://deb.nodesource.com/setup_20.x | sudo bash - && sudo apt-get install -y nodejs"
	case "uv":
		return "uv 安装失败。请手动执行: curl -LsSf https://astral.sh/uv/install.sh | sh, 或到 https://github.com/astral-sh/uv/releases 下载"
	case "python":
		if platform == "windows" {
			return "Python 未安装。请到 https://www.python.org/downloads/ 下载 3.12+ (安装时勾选 'Add to PATH')"
		}
		return "Python 未安装。请执行: sudo apt-get install -y python3 python3-pip"
	}
	return "请手动安装缺失组件后重试"
}

// tailLogFile 后台增量读日志文件到 installState.Logs (每 200ms)
func tailLogFile(path string, stop <-chan struct{}) {
	var offset int64
	for {
		select {
		case <-stop:
			return
		default:
		}
		offset = readLogIncremental(path, offset)
		time.Sleep(200 * time.Millisecond)
	}
}

// tailLogFileSync 一次性读完全部 (收尾用)
func tailLogFileSync(path string) {
	var offset int64
	for {
		n := readLogIncremental(path, offset)
		if n == offset {
			return
		}
		offset = n
	}
}

// readLogIncremental 读取文件从 offset 起的新增行, 追加到 Logs; 返回新 offset
func readLogIncremental(path string, offset int64) int64 {
	f, err := os.Open(path)
	if err != nil {
		return offset
	}
	defer f.Close()
	fi, _ := f.Stat()
	if fi == nil || fi.Size() <= offset {
		return offset
	}
	// 跳到 offset
	_, _ = f.Seek(offset, 0)
	data := make([]byte, fi.Size()-offset)
	n, _ := f.Read(data)
	if n == 0 {
		return offset
	}
	offset += int64(n)
	// 按行拆分追加
	for _, line := range strings.Split(string(data[:n]), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		installState.mu.Lock()
		installState.Logs = append(installState.Logs, line)
		installState.mu.Unlock()
	}
	return offset
}

// streamCmdOutput 实时把命令 stdout/stderr 流式写入 installState.Logs
func streamCmdOutput(stdout, stderr io.Reader) {
	stream := func(r io.Reader) {
		if r == nil {
			return
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			if line == "" {
				continue
			}
			installState.mu.Lock()
			installState.Logs = append(installState.Logs, line)
			installState.mu.Unlock()
		}
	}
	go stream(stdout)
	go stream(stderr)
}

// RegisterInstall 注册安装 API
func (h *Handler) RegisterInstall(cfg *config.Config) {
	h.HandleFunc("/api/install", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonErr(w, 405, "仅支持 POST")
			return
		}
		platform := detectPlatform()
		wechatDir := cfg.WechatBotDir
		astrbotRoot := cfg.AstrbotRoot
		wechatRepo := cfg.WechatBotRepo
		// 前端可能传 platform (浏览器系统), 但安装必须基于面板实际运行系统
		// (runtime.GOOS)。前端传的值仅在校验通过时作为提示, 不覆盖真实平台。
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if p, ok := body["platform"].(string); ok && p != "" {
			// 仅在面板真实平台无效时 (理论上不会) 使用前端值; 否则以 detectPlatform 为准
			_ = p
		}
		if d, ok := body["wechat_dir"].(string); ok && d != "" {
			wechatDir = d
		}
		if d, ok := body["astrbot_dir"].(string); ok && d != "" {
			astrbotRoot = d
		}
		if d, ok := body["wechat_repo"].(string); ok && d != "" {
			wechatRepo = strings.TrimSpace(d)
		}
		if platform != "windows" && platform != "mac" && platform != "linux" {
			jsonErr(w, 400, "未知平台: "+platform)
			return
		}
		tasks := planInstallTasks(platform, wechatDir, astrbotRoot, wechatRepo)
		if len(tasks) == 0 {
			jsonOK(w, map[string]any{
				"ok": true, "message": "所有组件已就绪, 无需安装", "tasks": []any{},
				"platform": platform, "wechat_dir": wechatDir, "astrbot_dir": astrbotRoot,
			})
			return
		}
		// 后台执行 (避免重复)
		installState.mu.Lock()
		if installState.Running {
			installState.mu.Unlock()
			jsonOK(w, map[string]any{"ok": true, "message": "安装已在后台进行中"})
			return
		}
		installState.mu.Unlock()
		go runInstall(tasks, platform, wechatDir, astrbotRoot)
		labels := make([]string, 0, len(tasks))
		for _, t := range tasks {
			labels = append(labels, t["label"])
		}
		jsonOK(w, map[string]any{
			"ok": true, "message": "开始安装 (" + platform + "): " + joinStr(labels),
			"tasks": tasks, "platform": platform, "wechat_dir": wechatDir, "astrbot_dir": astrbotRoot,
		})
	})

	h.HandleFunc("/api/install/status", func(w http.ResponseWriter, r *http.Request) {
		installState.mu.Lock()
		defer installState.mu.Unlock()
		logs := installState.Logs
		if logs == nil {
			logs = []string{}
		}
		stages := installState.Stages
		if stages == nil {
			stages = []StageState{}
		}
		jsonOK(w, map[string]any{
			"running": installState.Running, "logs": logs,
			"done": installState.Done, "ok": installState.OK,
			"platform": installState.Platform, "install_where": installState.Where,
			// 结构化进度 (v0.1.6+)
			"stage": installState.Stage, "stage_label": installState.StageLabel,
			"overall": installState.Overall, "stages": stages,
			"need_manual": installState.NeedManual, "manual_hint": installState.ManualHint,
		})
	})
}

func joinStr(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " / "
		}
		out += x
	}
	return out
}