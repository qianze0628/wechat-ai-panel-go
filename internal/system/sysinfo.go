// Package system 系统监控: CPU / 内存 / 磁盘 / 系统信息
package system

import (
	"os"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

// Info 系统状态 (对应 Python system_status())
type Info struct {
	CPU       *CPUInfo       `json:"cpu"`
	Memory    *MemoryInfo    `json:"memory"`
	Disk      *DiskInfo      `json:"disk"`
	System    *SystemInfo    `json:"system"`
	Uptime    int64          `json:"uptime"`
	Processes int            `json:"processes"`
	PanelPID  int            `json:"panel_pid"`
}

type CPUInfo struct {
	Cores          int     `json:"cores"`
	PhysicalCores  int     `json:"physical_cores"`
	UsagePercent   float64 `json:"usage_percent"`
	FreqMHz        float64 `json:"freq_mhz"`
}

type MemoryInfo struct {
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	Free         uint64  `json:"free"`
	UsagePercent float64 `json:"usage_percent"`
}

type DiskInfo struct {
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	Free         uint64  `json:"free"`
	UsagePercent float64 `json:"usage_percent"`
}

type SystemInfo struct {
	Platform string `json:"platform"`
	System   string `json:"system"`
	Release  string `json:"release"`
	Version  string `json:"version"`
	Machine  string `json:"machine"`
	Hostname string `json:"hostname"`
}

// Gather 采集系统信息 (错误降级为 nil)
func Gather() *Info {
	info := &Info{PanelPID: os.Getpid()}
	// CPU
	if c, err := cpu.Info(); err == nil && len(c) > 0 {
		info.CPU = &CPUInfo{
			Cores:         runtime.NumCPU(),
			PhysicalCores: physicalCores(),
			FreqMHz:       c[0].Mhz,
		}
	}
	if pct, err := cpu.Percent(200*time.Millisecond, false); err == nil && len(pct) > 0 {
		if info.CPU == nil {
			info.CPU = &CPUInfo{Cores: runtime.NumCPU()}
		}
		info.CPU.UsagePercent = pct[0]
	}
	// 内存
	if m, err := mem.VirtualMemory(); err == nil {
		info.Memory = &MemoryInfo{
			Total: m.Total, Used: m.Used, Free: m.Available, UsagePercent: m.UsedPercent,
		}
	}
	// 磁盘 (Windows C: / Linux /)
	diskPath := "C:\\"
	if runtime.GOOS != "windows" {
		diskPath = "/"
	}
	if d, err := disk.Usage(diskPath); err == nil {
		info.Disk = &DiskInfo{
			Total: d.Total, Used: d.Used, Free: d.Free, UsagePercent: d.UsedPercent,
		}
	}
	// 系统
	if hi, err := host.Info(); err == nil {
		info.System = &SystemInfo{
			Platform: hi.Platform,
			System:   hi.OS,
			Release:  hi.PlatformVersion,
			Version:  hi.KernelVersion,
			Machine:  hi.KernelArch,
			Hostname: hi.Hostname,
		}
		info.Uptime = int64(hi.Uptime)
	}
	info.Processes = processCount()
	return info
}

func physicalCores() int {
	n, err := cpu.Counts(false)
	if err != nil {
		return runtime.NumCPU()
	}
	return n
}

func processCount() int {
	return 0 // 留空 (避免额外依赖; 需要时可用 host 或 /proc)
}