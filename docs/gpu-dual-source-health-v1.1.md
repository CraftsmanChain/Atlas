# GPU 双源健康评分契约 v1.1

> 生效版本：`gpu-health-v1.1.0` / `feature-catalog v1.1.0`  
> 基线日期：2026-07-23

## 1. 目标与边界

`dcgm_exporter` 和 `gpu_exporter` 均为必需数据源，但不对语义等价指标重复计分：

1. DCGM 是规范健康特征的主源。
2. gpu_exporter 为等价特征提供降级备用，并提供 DCGM 当前没有的补充特征。
3. 两个源同时存在时执行一致性校验；偏差属于数据质量问题，不直接当作硬件故障扣分。
4. 只有 gpu_exporter 独有且具备明确硬件语义的特征进入新增健康规则。

现场基线为 DCGM 712 张实时 GPU、gpu_exporter 632 张实时 GPU。gpu_exporter 覆盖集合当前完全包含于 DCGM，暂无仅由 gpu_exporter 恢复的 GPU；回退能力主要覆盖单指标缺失及未来采集故障。

## 2. 规范化与优先级

等价特征在 PromQL 查询层完成单位归一化：

| 语义 | DCGM | gpu_exporter | 规范单位 |
| --- | --- | --- | --- |
| 温度 | `DCGM_FI_DEV_GPU_TEMP` | `nvidia_smi_temperature_gpu` | °C |
| 利用率 | `DCGM_FI_DEV_GPU_UTIL` | `nvidia_smi_utilization_gpu_ratio * 100` | % |
| SM/显存时钟 | MHz | Hz `/ 1000000` | MHz |
| 显存使用/空闲 | MiB | bytes `/ 1048576` | MiB |
| 功耗 | `DCGM_FI_DEV_POWER_USAGE` | `nvidia_smi_power_draw_watts` | W |
| Row Remap | DCGM Row Remap | nvidia-smi remapped rows | count/state |

UUID 同时兼容 DCGM 的 `UUID="GPU-..."` 和 gpu_exporter 的 `uuid="..."`，比较时移除可选 `GPU-` 前缀并统一大小写。

每个 snapshot 输出：

- `metric_sources`：每个规范特征最终采用的数据源。
- `sources_available`：本次 GPU 可用数据源。
- `fallback_metric_count`：DCGM 缺失而采用 gpu_exporter 的等价特征数。
- `consistency_issues` / `consistency_issue_count`：双源偏差及数量。

只要发生回退，数据置信度至少下降一级；存在双源不一致时再下降一级。回退不会导致同一特征重复扣分。

## 3. gpu_exporter 补充特征

Feature Catalog v1.1 包含 25 个规范健康特征和 41 个源查询。新增补充特征：

- `gpu_reset_required`
- `uncorrected_ecc_delta_24h`
- `fan_speed_pct`（当前仅 4090 现场支持）
- `pcie_link_width_current`
- `pcie_link_width_max`

新增确定性规则：

- `gpu_reset_required > 0`：稳定性 critical，扣 40。
- 24 小时不可纠正 aggregate ECC 增量大于 0：显存 critical，扣 30。
- GPU 15 分钟平均利用率不低于 80% 且当前 PCIe 宽度小于最大宽度：互联 warning，扣 15。

风扇特征先进入特征底座，不因单次高转速独立扣分，避免把正常散热响应误判为故障。

## 4. 一致性容差

一致性校验使用绝对容差和相对容差中的较大值：

- GPU 温度：3°C；显存温度：5°C。
- 功耗：25W 或 15%。
- GPU/显存利用率：10 个百分点。
- 时钟：150MHz 或 10%。
- 显存使用/空闲：256MiB 或 5%。
- Row Remap 状态与计数：要求一致。

超过容差时，问题中心生成 `gpu_source_inconsistency` 数据质量问题；恢复到容差内后自动关闭检测状态。人工处置记录和训练数据资格沿用问题中心现有流程。

## 5. 验收要求

- DCGM 有值时始终选 DCGM。
- DCGM 缺失且 gpu_exporter 有等价值时完成回退，并降低置信度。
- 等价特征只产生一个规范值和一次规则判断。
- 双源偏差不直接扣硬件分，但必须可见、可统计、可处置。
- gpu_exporter 独有规则必须有明确时间语义和负载保护。
- 页面显示回退 GPU 数、双源偏差 GPU 数及每卡来源详情。
