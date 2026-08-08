// Package process 服务守护: 启动时自动拉起 + 运行中健康检查自动恢复
//
// 背景: 电脑重启 / 面板重启后, AstrBot / wechat-bot / qr-server 不会自动拉起,
// 用户需手动逐个启动, 顺序或时机不对就出现"AI 不回/变蠢/链路断"。
// 本模块提供两级守护:
//   1. 面板启动后 EnsureAll() 自动拉起所有已就绪的服务 (幂等, 已在跑则跳过)
//   2. Supervise() 后台协程每 30s 健康检查, 掉线自动重启 (带冷却, 防抖动)
package process

import (
	"log"
	"sync"
	"time"
)

// ensureOnce 启动拉起只跑一次 (幂等)
var ensureOnce sync.Once

// EnsureAll 面板启动时调用: 自动拉起所有已就绪服务 (已在跑/未就绪的跳过)
// 返回本次实际启动的服务列表 (供日志)
func (s *Services) EnsureAll() []string {
	var started []string
	ensureOnce.Do(func() {
		for _, name := range []string{"astrbot", "wechat", "qr"} {
			ok, _ := s.HealthCheck(name)
			if ok {
				log.Printf("[supervisor] %s 已在运行, 跳过", name)
				continue
			}
			// 未就绪则不强行拉起 (如 wechat-bot 源码缺失时 Start 会返回错误)
			ok2, msg := s.Start(name)
			if ok2 {
				started = append(started, name)
				log.Printf("[supervisor] 已自动拉起 %s: %s", name, msg)
			} else {
				log.Printf("[supervisor] %s 启动失败 (稍后守护重试): %s", name, msg)
			}
		}
	})
	return started
}

// superviseStop 守护协程停止信号
var superviseStop = make(chan struct{})
var superviseOnce sync.Once

// Supervise 后台守护: 每 interval 健康检查一次, 掉线自动重启 (带冷却)
// 通过 go s.Supervise(30*time.Second) 启动
func (s *Services) Supervise(interval time.Duration) {
	superviseOnce.Do(func() {
		// 每服务上次重启时间 (冷却 5 分钟, 防止反复拉起失败死循环)
		cooldown := map[string]time.Time{}
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				for _, name := range []string{"astrbot", "wechat", "qr"} {
					ok, _ := s.HealthCheck(name)
					if ok {
						continue
					}
					// 未在跑 → 尝试拉起
					if last, has := cooldown[name]; has && time.Since(last) < 5*time.Minute {
						log.Printf("[supervisor] %s 掉线, 冷却期内跳过 (上次 %s)", name, last.Format("15:04:05"))
						continue
					}
					ok2, msg := s.Start(name)
					if ok2 {
						cooldown[name] = time.Now()
						log.Printf("[supervisor] %s 掉线已自动拉起: %s", name, msg)
					} else {
						cooldown[name] = time.Now()
						log.Printf("[supervisor] %s 自动拉起失败: %s", name, msg)
					}
				}
			case <-superviseStop:
				return
			}
		}
	})
}

// StopSupervise 停止守护 (进程退出时调用)
func (s *Services) StopSupervise() {
	select {
	case <-superviseStop:
	default:
		close(superviseStop)
	}
}
