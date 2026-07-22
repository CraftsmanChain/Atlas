# DCGM 指标能力盘点（2026-07-20）

## 1. 现场基线

- 资产库：90 个 GPU 节点、720 个 GPU 槽位、720 个已知 UUID。
- 当前 DCGM 实时序列：704 张 GPU。
- 型号覆盖：RTX 4090 264、H100 376、H200 64；另外 16 张卡仅能从历史数据恢复身份。
- 评分规则版本：`gpu-health-v1.0.0`。
- 健康评分周期：30 分钟；特征和评分历史保留 35 天。

## 2. 当前稳定实时指标

以下指标当前均有 704 条实时 GPU 序列：

- 温度：`GPU_TEMP`、`MEMORY_TEMP`
- 功耗与时钟：`POWER_USAGE`、`SM_CLOCK`、`MEM_CLOCK`
- 负载：`GPU_UTIL`、`MEM_COPY_UTIL`
- 显存：`FB_USED`、`FB_FREE`
- 稳定性：`XID_ERRORS`
- Row Remap：`CORRECTABLE_REMAPPED_ROWS`、`UNCORRECTABLE_REMAPPED_ROWS`、`ROW_REMAP_FAILURE`
- 互联：`PCIE_REPLAY_COUNTER`、`NVLINK_BANDWIDTH_TOTAL`
- 其他：编码/解码利用率、累计能耗、vGPU License 状态

RTX 4090 的 `MEMORY_TEMP` 当前固定为 0，V1 将其视为不支持，不纳入该型号的数据置信度分母。

## 3. 名称存在但当前无实时序列

- `CLOCK_THROTTLE_REASONS`
- `PCIE_LINK_WIDTH`
- `PSTATE`
- `APP_SM_CLOCK` / `APP_MEM_CLOCK`
- `POWER_VIOLATION` / `THERMAL_VIOLATION` / `RELIABILITY_VIOLATION`
- `GPU_NVLINK_ERRORS`

这些指标暂不进入 V1 扣分规则。后续应检查 DCGM Exporter collectors 配置和不同型号支持能力。

## 4. 现场分布与规则约束

- GPU 温度当前最大值：4090 78°C、H100 68°C、H200 40°C；15 分钟窗口无 GPU 达到 80°C。
- H100 显存温度当前最大 73°C，H200 最大 42°C；15 分钟窗口无卡达到 85°C。
- `XID_ERRORS` 当前非零不代表近期故障：该字段会保留最近一次 XID。V1 只在 `changes(XID_ERRORS[24h]) > 0` 时扣分；盘点时近 24 小时无变化。
- H100 当前 2 张卡 `UNCORRECTABLE_REMAPPED_ROWS > 0`，4 张卡 `CORRECTABLE_REMAPPED_ROWS > 0`。
- `PCIE_REPLAY_COUNTER` 是累计值。V1 只使用 1 小时增量；盘点时 95 分位为 0，最大增量约 2。
- 低时钟只有在 15 分钟平均 GPU 利用率不低于 80% 时才判定，避免把空闲降频当作算力衰减。

## 5. V1 输出

每张卡输出：

- `score`：0～100；数据不足时为 `null`。
- `level`：`healthy / attention / warning / critical / unknown`。
- `data_confidence`：A～D。
- 六维分数：稳定性、显存、温度、功耗、互联、性能。
- `evidence[]`、规则命中明细、规则版本和计算时间。

V1 不使用大模型直接评分，不把异常分包装成故障概率，不在缺少任务上下文时依据单次低利用率或低功耗扣分。
