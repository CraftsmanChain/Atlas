package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"atlas/internal/collector"
)

func main() {
	fmt.Println("Starting Atlas Agent...")

	metricsCol := collector.NewMetricsCollector()
	gatewayURL := "http://localhost:8080/api/v1/push/metrics" // 默认网关地址

	// Agent 持续运行并定期采集数据的过程
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case t := <-ticker.C:
			metrics := metricsCol.Collect()
			fmt.Printf("[%s] Agent: Collected metrics - CPU: %.2f%%, Mem: %.2f%%\n", 
				t.Format("15:04:05"), metrics.CPUUsage, metrics.MemoryUsage)
			
			// 将采集到的数据推送到网关
			payload, err := json.Marshal(metrics)
			if err != nil {
				fmt.Printf("Failed to marshal metrics: %v\n", err)
				continue
			}

			resp, err := http.Post(gatewayURL, "application/json", bytes.NewBuffer(payload))
			if err != nil {
				fmt.Printf("Failed to push metrics to gateway: %v\n", err)
				continue
			}
			resp.Body.Close()
		}
	}
}
