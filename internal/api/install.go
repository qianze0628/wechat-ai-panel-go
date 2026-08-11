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

// buildCmdEnv 构造子进程 env: 注册表最新 PATH + 额外目录 + 镜像变量。
// 修复 (2026-08-10): 之前用 os.Environ() 快照 → winget 装 node 后 npm 阶段找不到 node。
func buildCmdEnv(extraPaths []string) []string {
	sysPath := refreshSystemPath()
	// extraPaths 是"安装目录列表" (如 %USERPROFILE%\.local\bin), 作为 PATH 前缀
	// 修复: 逆序遍历保持传入顺序 (否则后追加的排最前)
	for i := len(extraPaths) - 1; i >= 0; i-- {
		p := extraPaths[i]
		if p != "" && !strings.Contains(sysPath, p) {
			sysPath = p + string(os.PathListSeparator) + sysPath
		}
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			continue // 丢弃快照 PATH, 用刷新后的
		}
		env = append(env, kv)
	}
	env = append(env, "PATH="+sysPath)
	env = append(env, "PYTHONIOENCODING=utf-8")
	if mirrorPypiIndex != "" {
		env = append(env, "UV_INDEX_URL="+mirrorPypiIndex, "PIP_INDEX_URL="+mirrorPypiIndex)
	}
	// 修复 (2026-08-10): uv 首次安装工具需要下载 CPython runtime (github.com/astral-sh/python-build-standalone),
	// 国内直连超时 → 设置 UV_PYTHON_INSTALL_MIRROR 走 gh-proxy 镜像 (与 uv 二进制下载同源)
	env = append(env, "UV_PYTHON_INSTALL_MIRROR=https://gh-proxy.com/https://github.com/astral-sh/python-build-standalone")
	return env
}

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

// installLogPath 安装日志文件路径 (持久化, 日志页可回查)
var installLogPath = ""

// SetInstallLogPath 由 main.go 注入 (与面板其他日志同目录)
func SetInstallLogPath(path string) { installLogPath = path }

// 国内镜像源 (由 main.go 从配置注入; 空=直连)
var (
	mirrorNpmRegistry   = ""
	mirrorPypiIndex     = ""
	mirrorGitCloneProxy = ""
)

// SetMirrors 注入镜像源配置
func SetMirrors(npm, pypi, gitProxy string) {
	mirrorNpmRegistry = npm
	mirrorPypiIndex = pypi
	mirrorGitCloneProxy = gitProxy
}

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
	// git: 全新电脑常无 git → clone 阶段必失败。Windows 装便携 git (面板管理 PATH);
	// 其他平台提示系统包管理器安装。
	// 修复 (2026-08-10): 之前完全没检查 git。
	if which2("git") == "" {
		tasks = append(tasks, map[string]string{
			"label": envInstallLabel(platform, "git"),
			"kind":  "env_git", "target": "",
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
			// 修复 (2026-08-10): 之前用 winget install OpenJS.NodeJS.LTS — 依赖微软商店/网络,
			// 全新电脑无商店应用/网络差时必失败; 且装到 Program Files 当前进程 PATH 快照找不到。
			// 改为: 直接下载 Node.js 便携版 zip (官方/淘宝镜像), 解压到面板 runtime/nodejs,
			// 由面板自己管理 PATH (每次 runCmd 前注入), 完全不依赖商店与系统 PATH。
			return exec.Command("powershell", "-NoProfile", "-Command", buildNodePortableCmd())
		case "mac":
			return exec.Command("brew", "install", "node")
		default: // linux
			return exec.Command("bash", "-c", "curl -fsSL https://deb.nodesource.com/setup_20.x | sudo bash - && sudo apt-get install -y nodejs")
		}
	case "uv":
		// uv 安装: 多候选源 (GitHub Release 直连/ghproxy/astral 官网), 任一成功即可。
		// 装完后 runCmd 里会刷新 PATH 并真实检测 uv 可用, 而不是只看命令退出码。
		if platform == "windows" {
			// Windows: 下载官方 uv.exe (zip) 解压到 %USERPROFILE%\.local\bin
			// 按架构选 url (amd64 常见; arm64 备用)
			arch := "x86_64-pc-windows-msvc"
			if runtime.GOARCH == "arm64" {
				arch = "aarch64-pc-windows-msvc"
			}
			uvVer := "0.6.14" // 稳定版本 (更新时同步改)
			// 修复 (2026-08-10): 增加国内可达源: astral.sh 官方 (大陆可直连) + npmmirror 镜像 + gh-proxy
			urlAstral := "https://astral.sh/uv/" + uvVer + "/uv-" + arch + ".zip"
			urlMirror := "https://gh-proxy.com/https://github.com/astral-sh/uv/releases/download/" + uvVer + "/uv-" + arch + ".zip"
			urlNpmmirror := "https://npmmirror.com/mirrors/uv/uv-" + arch + ".zip"
			urlDirect := "https://github.com/astral-sh/uv/releases/download/" + uvVer + "/uv-" + arch + ".zip"
			// 镜像配置里的 git clone proxy 也可用作文件下载加速前缀
			urlGhfast := ""
			if mirrorGitCloneProxy != "" {
				urlGhfast = mirrorGitCloneProxy + "https://github.com/astral-sh/uv/releases/download/" + uvVer + "/uv-" + arch + ".zip"
			}
			// 用 PowerShell 依次尝试 (astral官方→npmmirror→ghfast→gh-proxy→直连), 解压 zip 到 %USERPROFILE%\.local\bin
			// 修复: 全英文输出避免 GBK 乱码; 目标 uv.exe 共存时先改名旧的再复制; 最后必须 Test-Path 成功才 exit 0
			ps := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $ErrorActionPreference = 'Stop'; " +
				"$target = Join-Path $env:USERPROFILE '.local\\bin'; " +
				"New-Item -ItemType Directory -Force -Path $target | Out-Null; " +
				"$zip = Join-Path $env:TEMP 'uv-installer.zip'; " +
				"$urls = @('" + urlAstral + "', '" + urlNpmmirror + "'); " +
				"if ('" + urlGhfast + "' -ne '') { $urls = @('" + urlGhfast + "') + $urls }; " +
				"$urls = $urls + @('" + urlMirror + "', '" + urlDirect + "'); " +
				"$ok = $false; " +
				"foreach ($u in $urls) { try { Invoke-WebRequest -Uri $u -OutFile $zip -UseBasicParsing -TimeoutSec 45; if ((Get-Item $zip -ErrorAction SilentlyContinue).Length -gt 100000) { $ok = $true; break } } catch { Write-Output ('[download-fail] ' + $u); Remove-Item $zip -Force -ErrorAction SilentlyContinue } }; " +
				"if (-not $ok) { Write-Error 'uv download failed from all sources. Manual install: https://github.com/astral-sh/uv/releases'; exit 1 }; " +
				"Add-Type -AssemblyName System.IO.Compression.FileSystem; " +
				"$tmp = Join-Path $env:TEMP ('uv' + [guid]::NewGuid().ToString('N')); " +
				"[System.IO.Compression.ZipFile]::ExtractToDirectory($zip, $tmp); " +
				"$probe = Join-Path $target 'uv.exe'; " +
				"if (Test-Path $probe) { Rename-Item $probe ($probe + '.old') -Force -ErrorAction SilentlyContinue }; " +
				"Copy-Item -Path (Join-Path $tmp 'uv.exe') -Destination $target -Force; " +
				"Copy-Item -Path (Join-Path $tmp 'uvx.exe') -Destination $target -Force -ErrorAction SilentlyContinue; " +
				"Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue; " +
				"if (-not (Test-Path $probe)) { Write-Error 'uv.exe extract failed'; exit 1 }; " +
				// 修复 (2026-08-10): 把 .local\bin 写入用户 PATH, 面板重启/用户终端都能用 uv
				"$userPath = [Environment]::GetEnvironmentVariable('Path', 'User'); " +
				"if ($userPath -notlike '*\\.local\\bin*') { [Environment]::SetEnvironmentVariable('Path', $userPath + ';' + $target, 'User') }; " +
				"Write-Output 'uv install OK'; exit 0"
			return exec.Command("powershell", "-NoProfile", "-Command", ps)
		}
		// Linux/macOS: 官方脚本 (带 ghproxy 镜像加速), 失败提示手动
		if mirrorGitCloneProxy != "" {
			return exec.Command("bash", "-c",
				"curl -LsSf "+mirrorGitCloneProxy+"https://github.com/astral-sh/uv/install.sh | sh || curl -LsSf https://astral.sh/uv/install.sh | sh")
		}
		return exec.Command("bash", "-c", "curl -LsSf https://astral.sh/uv/install.sh | sh || curl -LsSf "+"https://raw.githubusercontent.com/astral-sh/uv/main/install.sh | sh")
	case "python":
		switch platform {
		case "windows":
			return exec.Command("cmd", "/c", "chcp 65001>nul && winget install --id Python.Python.3.12 --accept-source-agreements --accept-package-agreements --silent")
		case "mac":
			return exec.Command("brew", "install", "python@3.12")
		default:
			return exec.Command("bash", "-c", "sudo apt-get install -y python3 python3-pip")
		}
	case "git":
		if platform == "windows" {
			// 便携 MinGit (面板管理 PATH), 不依赖系统安装
			return exec.Command("powershell", "-NoProfile", "-Command", gitPortableCmd())
		}
		return exec.Command("bash", "-c", "sudo apt-get install -y git || brew install git")
	}
	return nil
}

// nodePortableDir 面板自管理 Node.js 便携目录 (随面板分发, 不依赖系统 PATH)
func nodePortableDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".wechat-ai-panel", "nodejs")
}

// buildNodePortableCmd 构造 PowerShell 命令: 下载 Node.js LTS 便携版 zip 并解压到面板目录。
// 源: 淘宝镜像 (npmmirror, 国内快) 优先, 官方 nodejs.org 兜底。
// 修复 (2026-08-10): 不依赖 winget/商店; 面板注入 PATH 使用。
func buildNodePortableCmd() string {
	nodeVer := "v22.14.0" // LTS (更新时同步改)
	arch := "x64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	target := nodePortableDir()
	zipName := "node-" + nodeVer + "-win-" + arch
	urlMirror := "https://npmmirror.com/mirrors/node/" + nodeVer + "/" + zipName + ".zip"
	urlDirect := "https://nodejs.org/dist/" + nodeVer + "/" + zipName + ".zip"
	ps := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $ErrorActionPreference = 'Stop'; " +
		"$target = '" + target + "'; " +
		"New-Item -ItemType Directory -Force -Path $target | Out-Null; " +
		"$zip = Join-Path $env:TEMP 'node-installer.zip'; " +
		"$urls = @('" + urlMirror + "', '" + urlDirect + "'); " +
		"$ok = $false; " +
		"foreach ($u in $urls) { try { Invoke-WebRequest -Uri $u -OutFile $zip -UseBasicParsing -TimeoutSec 300; if ((Get-Item $zip -ErrorAction SilentlyContinue).Length -gt 10000000) { $ok = $true; break } } catch { Write-Output ('[download-fail] ' + $u); Remove-Item $zip -Force -ErrorAction SilentlyContinue } }; " +
		"if (-not $ok) { Write-Error 'node download failed from all sources. Manual: https://nodejs.org'; exit 1 }; " +
		"Add-Type -AssemblyName System.IO.Compression.FileSystem; " +
		"$tmp = Join-Path $env:TEMP ('node' + [guid]::NewGuid().ToString('N')); " +
		"[System.IO.Compression.ZipFile]::ExtractToDirectory($zip, $tmp); " +
		"$src = Join-Path $tmp '" + zipName + "'; " +
		"if (-not (Test-Path (Join-Path $src 'node.exe'))) { Write-Error 'node.exe not in zip'; exit 1 }; " +
		"Get-ChildItem $src | Copy-Item -Destination $target -Recurse -Force; " +
		"Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue; " +
		"if (-not (Test-Path (Join-Path $target 'node.exe'))) { Write-Error 'node extract failed'; exit 1 }; " +
		"Write-Output 'node portable OK'; exit 0"
	return ps
}

// gitPortableCmd 构造 PowerShell 命令: 下载 Git for Windows 便携版 (MinGit) 到面板目录。
// 修复 (2026-08-10): 全新电脑无 git → clone 阶段必失败; 便携 git 由面板注入 PATH。
func gitPortableCmd() string {
	home, _ := os.UserHomeDir()
	target := filepath.Join(home, ".wechat-ai-panel", "git")
	zipName := "MinGit-2.47.1-64-bit"
	// 修复 (2026-08-10): npmmirror 路径含版本子目录; gh-proxy 兜底
	urlNpmmirror := "https://npmmirror.com/mirrors/git-for-windows/v2.47.1.windows.1/" + zipName + ".zip"
	urlGhproxy := "https://gh-proxy.com/https://github.com/git-for-windows/git/releases/download/v2.47.1.windows.1/" + zipName + ".zip"
	urlDirect := "https://github.com/git-for-windows/git/releases/download/v2.47.1.windows.1/" + zipName + ".zip"
	ps := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $ErrorActionPreference = 'Stop'; " +
		"$target = '" + target + "'; " +
		"New-Item -ItemType Directory -Force -Path $target | Out-Null; " +
		"$zip = Join-Path $env:TEMP 'git-installer.zip'; " +
		"$urls = @('" + urlNpmmirror + "', '" + urlGhproxy + "', '" + urlDirect + "'); " +
		"$ok = $false; " +
		"foreach ($u in $urls) { try { Invoke-WebRequest -Uri $u -OutFile $zip -UseBasicParsing -TimeoutSec 300; if ((Get-Item $zip -ErrorAction SilentlyContinue).Length -gt 1000000) { $ok = $true; break } } catch { Write-Output ('[download-fail] ' + $u); Remove-Item $zip -Force -ErrorAction SilentlyContinue } }; " +
		"if (-not $ok) { Write-Error 'git download failed from all sources. Manual: https://git-scm.com/downloads'; exit 1 }; " +
		"Add-Type -AssemblyName System.IO.Compression.FileSystem; " +
		"$tmp = Join-Path $env:TEMP ('git' + [guid]::NewGuid().ToString('N')); " +
		"[System.IO.Compression.ZipFile]::ExtractToDirectory($zip, $tmp); " +
		"Get-ChildItem $tmp | Copy-Item -Destination $target -Recurse -Force; " +
		"Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue; " +
		"if (-not (Test-Path (Join-Path $target 'cmd\\git.exe'))) { Write-Error 'git extract failed'; exit 1 }; " +
		"Write-Output 'git portable OK'; exit 0"
	return ps
}

// envInstallLabel 返回环境依赖的安装提示 (平台感知)
func envInstallLabel(platform, name string) string {
	switch name {
	case "node":
		switch platform {
		case "windows":
			// 修复 (2026-08-10): 便携版自动下载, 不依赖 winget 商店
			return "安装 Node.js: 自动下载便携版 (npmmirror 镜像)"
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
	case "git":
		if platform == "windows" {
			return "安装 git: 自动下载便携 MinGit (面板管理)"
		}
		return "安装 git: sudo apt-get install -y git"
	}
	return "安装 " + name
}

// runInstall 后台执行安装 (分阶段执行器: 环境 → clone → npm → astrbot → 验证)
// 每阶段有结构化状态 (Stages), 总进度 Overall 按阶段权重推进 (参考 AstrBot UpdateProgress)
func runInstall(tasks []map[string]string, platform, wechatDir, astrbotRoot string) {
	// 清空历史安装日志文件 (每次全新安装; 路径由 SetInstallLogPath 注入)
	if installLogPath != "" {
		_ = os.MkdirAll(filepath.Dir(installLogPath), 0o755)
		_ = os.WriteFile(installLogPath, []byte(""), 0o644)
	}
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
	// 安装日志也持久化到 logs/install.log (日志页可回查, 重启不丢)
	// 路径由 SetInstallLogPath 注入 (main.go), 与主日志同目录
	addLog := func(line string) {
		installState.mu.Lock()
		installState.Logs = append(installState.Logs, line)
		installState.mu.Unlock()
		// 追加到文件 (失败也保留, 供调试)
		_ = os.MkdirAll(filepath.Dir(installLogPath), 0o755)
		f, err := os.OpenFile(installLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			_, _ = f.WriteString(line + "\n")
			_ = f.Close()
		}
	}

	// 运行命令并捕获输出; 返回 (ok, errMsg)
	// 注意: 不能用 cmd.Run() + StdoutPipe 组合 —— npm install 的 prepare 钩子 (husky/npx)
	// 会派生孙进程继承管道, Run() 会等孙进程退出导致卡死。用文件重定向最可靠。
	runCmd := func(cmd *exec.Cmd, timeout time.Duration) (bool, string) {
		if cmd == nil {
			return false, "命令为空"
		}
		cmd.Env = buildCmdEnv(extraPaths)
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
			// 修复 (2026-08-10): 只 Kill 主进程会残留孙进程 (npm install 的 husky/child),
			// Windows 用 taskkill /T /F 杀整棵树
			if runtime.GOOS == "windows" {
				_ = exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
			}
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
		if strings.HasPrefix(t["kind"], "env_") {
			addLog("[env] [start] " + t["label"])
			cmd := envInstallCmd(platform, strings.TrimPrefix(t["kind"], "env_"))
			ok2, errMsg := runCmd(cmd, 300*time.Second)
			if ok2 {
				addLog("[env] [done] " + t["label"] + " exit=0")
				// 安装目录加入额外 PATH 前缀 (优先级最高; 与注册表 PATH 叠加)
				home, _ := os.UserHomeDir()
				extraPaths = append(extraPaths,
					filepath.Join(home, ".local", "bin"),
					nodePortableDir(),
					filepath.Join(home, ".wechat-ai-panel", "git", "cmd"),
					filepath.Join(home, ".wechat-ai-panel", "git", "mingw64", "bin"),
					filepath.Join(home, ".wechat-ai-panel", "git", "usr", "bin"),
				)
			} else {
				envOK = false
				addLog("[env] [error] " + t["label"] + " FAILED: " + errMsg)
				installState.mu.Lock()
				installState.NeedManual = true
				// 修复 (2026-08-10): 多工具失败时累加 hint, 避免后一个覆盖前一个
				if installState.ManualHint == "" {
					installState.ManualHint = toolManualHint(platform, strings.TrimPrefix(t["kind"], "env_"))
				} else {
					installState.ManualHint += "\n---\n" + toolManualHint(platform, strings.TrimPrefix(t["kind"], "env_"))
				}
				installState.mu.Unlock()
			}
		}
	}
	// 重新检测 (PATH 刷新后; 每个缺失项必须真实可用, 不能只看命令退出码)
	checkEnv := func(name string) bool {
		switch name {
		case "node":
			return which2("node") != "" && which2("npm") != ""
		case "uv":
			return which2("uv") != ""
		case "python":
			return which2("python") != "" || which2("python3") != ""
		case "git":
			return which2("git") != ""
		}
		return true
	}
	for _, t := range tasks {
		if strings.HasPrefix(t["kind"], "env_") {
			nm := strings.TrimPrefix(t["kind"], "env_")
			if !checkEnv(nm) {
				envOK = false
				addLog("[env] [error] " + nm + " 安装后检测仍不可用, 请手动安装")
				installState.mu.Lock()
				installState.NeedManual = true
				if installState.ManualHint == "" {
					installState.ManualHint = toolManualHint(platform, nm)
				} else {
					installState.ManualHint += "\n---\n" + toolManualHint(platform, nm)
				}
				installState.mu.Unlock()
			}
		}
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
		// 前置检查 (2026-08-11): git 不可用时报明确中文错误, 避免"git not found"模糊失败
		// (便携 MinGit 安装后 which2 检测不到 → 之前误报"环境就绪"但 clone 却失败)
		if which2("git") == "" {
			addLog("[clone] [error] git 命令不可用 (环境检测失败)。请先在「环境」步骤安装 git 或手动安装: https://git-scm.com/downloads")
			stageDone("clone", "git 不可用", "git 命令不存在, 无法 clone")
			installState.mu.Lock()
			installState.NeedManual = true
			installState.ManualHint = "git 不可用 (便携安装后检测失败)。\n手动方案: ① 到 https://git-scm.com/downloads 安装 Git;\n② 或用浏览器打开 https://github.com/qianze0628/wechat-bot-optimized 点 Code → Download ZIP, 解压后重命名为 wechat-bot-windows 放到本程序目录"
			installState.mu.Unlock()
			finishInstall(false)
			return
		}
		addLog("[clone] [start] " + cloneTask["label"])
		_ = os.MkdirAll(wechatDir, 0o755)
		repo := cloneTask["repo"]
		// 国内加速: 多级镜像候选自动回退 (修复 2026-08-10: 之前默认镜像为空 → 国内直连 github 必失败)
		// 候选顺序: 用户配置镜像 → 内置公共镜像列表 → 直连
		cloneOK := false
		var cloneErr string
		repoClean := strings.TrimPrefix(strings.TrimPrefix(repo, "https://"), "http://")
		proxyCandidates := []string{}
		if mirrorGitCloneProxy != "" {
			proxyCandidates = append(proxyCandidates, mirrorGitCloneProxy)
		}
		proxyCandidates = append(proxyCandidates,
			"https://gh-proxy.com/",
			"https://ghfast.top/",
			"https://ghproxy.net/",
			"https://mirror.ghproxy.com/",
		)
		for _, proxy := range proxyCandidates {
			if cloneOK {
				break
			}
			proxied := strings.TrimSuffix(proxy, "/") + "/" + repoClean
			addLog("[clone] [info] 尝试镜像: " + proxied)
			_ = os.RemoveAll(wechatDir)
			_ = os.MkdirAll(wechatDir, 0o755)
			cmd := exec.Command("git", "clone", "--depth", "1", proxied, wechatDir)
			var ok2 bool
			ok2, cloneErr = runCmd(cmd, 240*time.Second)
			if ok2 {
				cloneOK = true
				cloneErr = ""
			} else {
				addLog("[clone] [warn] 镜像失败: " + cloneErr)
			}
		}
		if !cloneOK {
			_ = os.RemoveAll(wechatDir)
			_ = os.MkdirAll(wechatDir, 0o755)
			cmd := exec.Command("git", "clone", "--depth", "1", repo, wechatDir)
			ok2, errMsg := runCmd(cmd, 240*time.Second)
			if ok2 {
				cloneOK = true
				cloneErr = ""
			} else {
				cloneErr = errMsg
			}
		}
		if !cloneOK {
			stageDone("clone", "git clone 失败", cloneErr)
			installState.mu.Lock()
			installState.NeedManual = true
			installState.ManualHint = "git clone 失败: " + cloneErr +
				"\n已尝试国内镜像 (gh-proxy/ghfast 等) 与直连均失败, 请检查网络或稍后重试。" +
				"\n手动方案: ① 若未装 git, 到 https://git-scm.com/downloads 下载安装;" +
				"\n② 用浏览器打开 https://github.com/qianze0628/wechat-bot-optimized 点 Code → Download ZIP," +
				"解压后把文件夹重命名为 wechat-bot-windows 放到本程序目录下, 再点\"重新检测\""
			installState.mu.Unlock()
			finishInstall(false)
			return
		}
		// 修复 (2026-08-10): clone 成功后必须校验 package.json 存在, 否则半成品目录
		// (超时被杀/网络中断留下 .git 无源码) 会让后续 npm 阶段静默跳过 → 安装"成功"但 bot 不可用
		if !fileExists(filepath.Join(wechatDir, "package.json")) {
			_ = os.RemoveAll(wechatDir)
			stageDone("clone", "源码不完整 (无 package.json)", "clone 半成品, 已清理, 请重试")
			installState.mu.Lock()
			installState.NeedManual = true
			installState.ManualHint = "源码拉取不完整 (可能网络中断)。已清理半成品目录, 请重新点击安装重试。"
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
		args := []string{"install"}
		if mirrorNpmRegistry != "" {
			args = append(args, "--registry="+mirrorNpmRegistry)
		}
		cmd := exec.Command("npm", args...)
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
	// 修复 (2026-08-10): 之前用 exec.LookPath (面板进程 PATH 快照) → 装好的工具全找不到 → 误报失败。
	// 改用 which2 (含已知目录回退) + findAstrbotExePath (uv tools 目录回退)。
	setStage("verify", "验证", 0)
	nodeV := which2("node")
	uvPath := which2("uv")
	astrbotPath := findAstrbotExePath()
	detail := "node=" + ternary(nodeV != "", "✓", "✗") +
		" uv=" + ternary(uvPath != "", "✓", "✗") +
		" astrbot=" + ternary(astrbotPath != "", "✓", "✗")
	if nodeV != "" && uvPath != "" && astrbotPath != "" {
		addLog("[verify] [done] " + detail)
		stageDone("verify", detail, "")
		ok = true
	} else {
		addLog("[verify] [error] " + detail)
		stageDone("verify", detail, "部分组件验证未通过 (若工具已安装, 重启面板后生效)")
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
		if platform == "windows" {
			return "uv 安装失败 (下载源可能被墙/断网)。请手动: ① 浏览器打开 https://github.com/astral-sh/uv/releases 下载 uv-x86_64-pc-windows-msvc.zip; ② 解压得到 uv.exe; ③ 放到 C:\\Users\\你的用户名\\.local\\bin\\ 下; ④ 点\"重新检测\""
		}
		return "uv 安装失败。请手动执行: curl -LsSf https://astral.sh/uv/install.sh | sh, 或到 https://github.com/astral-sh/uv/releases 下载, 完成后点\"重新检测\""
	case "python":
		if platform == "windows" {
			return "Python 未安装。请到 https://www.python.org/downloads/ 下载 3.12+ (安装时勾选 'Add to PATH')"
		}
		return "Python 未安装。请执行: sudo apt-get install -y python3 python3-pip"
	case "git":
		if platform == "windows" {
			return "git 安装失败 (下载源不可达)。请手动到 https://git-scm.com/downloads 下载安装 (默认选项即可), 完成后点\"重新检测\""
		}
		return "git 未安装。请执行: sudo apt-get install -y git"
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
		// 同步写 install.log (供日志页回查)
		if installLogPath != "" {
			if f, err := os.OpenFile(installLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				_, _ = f.WriteString(line + "\n")
				_ = f.Close()
			}
		}
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