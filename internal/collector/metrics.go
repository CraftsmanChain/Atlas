package collector

import (
	"log"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"

	"atlas/pkg/api"
)

// MetricsCollector 负责收集宿主机系统级别的基础指标
type MetricsCollector struct {
	hostname string
}

func NewMetricsCollector() *MetricsCollector {
	hInfo, err := host.Info()
	hostname := "unknown-host"
	if err == nil && hInfo.Hostname != "" {
		hostname = hInfo.Hostname
	}
	return &MetricsCollector{
		hostname: hostname,
	}
}

// Collect 使用 gopsutil 收集当前系统的 CPU、内存和磁盘利用率，支持跨平台 (Ubuntu, CentOS, macOS 等)
func (c *MetricsCollector) Collect() *api.SystemMetrics {
	var cpuUsage, memUsage, diskUsage float64

	// 采集 CPU (1秒钟的平均使用率)
	cpuPercents, err := cpu.Percent(time.Second, false)
	if err == nil && len(cpuPercents) > 0 {
		cpuUsage = cpuPercents[0]
	} else {
		log.Printf("Error collecting CPU metrics: %v", err)
	}

	// 采集内存
	vMem, err := mem.VirtualMemory()
	if err == nil {
		memUsage = vMem.UsedPercent
	} else {
		log.Printf("Error collecting Memory metrics: %v", err)
	}

	// 采集磁盘 (根目录)
	// 在不同系统上，根路径可能不同，Unix系为 "/"，如果需要跨平台兼容Windows可能需要适配
	dUsage, err := disk.Usage("/")
	if err == nil {
		diskUsage = dUsage.UsedPercent
	} else {
		log.Printf("Error collecting Disk metrics: %v", err)
	}

	return &api.SystemMetrics{
		Host:        c.hostname,
		CPUUsage:    cpuUsage,
		MemoryUsage: memUsage,
		DiskUsage:   diskUsage,
		Timestamp:   time.Now(),
	}
}
