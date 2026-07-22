# 资产与监控覆盖核查（2026-07-17）

- 采集时间：2026-07-17T13:58:55+08:00
- 资产文件：`/Users/chuan/Downloads/智元主机数据.xlsx`
- 查询接口：`http://10.111.201.1:9090/api/v1/query`
- 检查 jobs：`node_exporter,dcgm_exporter,gpu_exporter,ipmi_exporter`
- 资产节点数：103
- 监控目标 IP 数：386
- 资产文件有、监控没有：0
- 监控有、资产文件没有：16
- 状态非 UP 记录数：13

## 问题统计

| 类型 | 数量 |
|---|---:|
| dcgm_exporter_DOWN | 1 |
| dcgm_exporter_MISSING | 1 |
| gpu_exporter_DOWN | 11 |
| gpu_exporter_MISSING | 1 |
| monitor_has_but_asset_missing | 16 |
| node_exporter_DOWN | 1 |
| node_exporter_MISSING | 1 |

## 资产文件有、监控没有

| 节点 | BMC | 名称 | 平台状态 | 监控 Agent |
|---|---|---|---|---|

## 监控有、资产文件没有

| 目标 IP | jobs | 映射资产节点 |
|---|---|---|
| 10.111.51.100 | ipmi_exporter |  |
| 10.111.52.1 | ipmi_exporter |  |
| 10.111.52.2 | ipmi_exporter |  |
| 10.111.52.3 | ipmi_exporter |  |
| 10.111.52.4 | ipmi_exporter |  |
| 10.111.52.9 | ipmi_exporter |  |
| 10.114.1.1 | ipmi_exporter |  |
| 10.114.1.2 | ipmi_exporter |  |
| 10.114.1.3 | ipmi_exporter |  |
| 10.114.1.6 | ipmi_exporter |  |
| 10.114.1.7 | ipmi_exporter |  |
| 10.114.1.8 | ipmi_exporter |  |
| 10.114.1.9 | ipmi_exporter |  |
| 10.114.1.10 | ipmi_exporter |  |
| 10.114.1.11 | ipmi_exporter |  |
| 10.114.1.37 | ipmi_exporter |  |

## 状态不是 UP 的资产节点

| 节点 | BMC | 名称 | node | dcgm | gpu | ipmi |
|---|---|---|---|---|---|---|
| 10.111.101.3 |  | lexun-harbor01 | DOWN | N/A | N/A | N/A |
| 10.114.4.25 | 10.114.1.25 | 4090GPU-05 | MISSING | MISSING | MISSING | UP |
| 10.114.4.130 | 10.114.1.130 | h100gpu-30 | UP | DOWN | DOWN | UP |
| 10.114.4.132 | 10.114.1.132 | h100gpu-32 | UP | UP | DOWN | UP |
| 10.114.4.135 | 10.114.1.135 | h100gpu-35 | UP | UP | DOWN | UP |
| 10.114.4.139 | 10.114.1.139 | h100gpu-39 | UP | UP | DOWN | UP |
| 10.114.4.201 | 10.114.1.201 | H200gpu-01 | UP | UP | DOWN | UP |
| 10.114.4.202 | 10.114.1.202 | H200gpu-02 | UP | UP | DOWN | UP |
| 10.114.4.203 | 10.114.1.203 | H200gpu-03 | UP | UP | DOWN | UP |
| 10.114.4.204 | 10.114.1.204 | H200gpu-04 | UP | UP | DOWN | UP |
| 10.114.4.205 | 10.114.1.205 | H200gpu-05 | UP | UP | DOWN | UP |
| 10.114.4.206 | 10.114.1.206 | H200gpu-06 | UP | UP | DOWN | UP |
| 10.114.4.207 | 10.114.1.207 | H200gpu-07 | UP | UP | DOWN | UP |

## 文件

- `asset-monitor-compare-2026-07-17.csv`：全量对比
- `asset-has-monitor-missing-2026-07-17.csv`：资产文件有、监控没有
- `monitor-has-asset-missing-2026-07-17.csv`：监控有、资产文件没有
- `monitor-status-not-up-2026-07-17.csv`：状态不是 UP 的节点
- `asset-monitor-report-2026-07-17.md`：摘要报告
