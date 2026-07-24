# GPU 双源健康评分契约 v1.4

> 生效版本：`gpu-health-v1.4.1` / `feature-catalog v1.4.0`
> 基线日期：2026-07-24

## 1. 目标与边界

`dcgm_exporter` 和 `gpu_exporter` 均为必需数据源，但不对语义等价指标重复计分：

1. DCGM 是规范健康特征的主源。
2. gpu_exporter 为等价特征提供降级备用，并提供 DCGM 当前没有的补充特征。
3. 两个源同时存在时记录规范化后的数值差异；差异仅用于审计，不属于数据质量问题或硬件故障。
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
- `consistency_candidates` / `consistency_candidate_count`：本轮双源数值差异，仅用于来源审计。
- `consistency_issues` / `consistency_issue_count`：兼容保留字段，自 v1.1.2 起稳定输出空值/0，不再表示异常。

只要发生回退，数据置信度至少下降一级。双源数值差异不影响置信度，回退也不会导致同一特征重复扣分。

## 3. gpu_exporter 补充特征

Feature Catalog v1.4 包含 35 个规范健康特征和 53 个源查询。补充、趋势及结构特征包括：

- `gpu_reset_required`
- `uncorrected_ecc_delta_24h`
- `fan_speed_pct`（当前仅 4090 现场支持）
- `pcie_link_width_current`
- `pcie_link_width_max`
- `correctable_remapped_rows_delta_1h`
- `correctable_remapped_rows_delta_24h`
- `gpu_metric_samples_1h`
- `gpu_metric_presence_ratio_1h`
- `gpu_metric_sample_age_seconds`
- `gpu_metric_gap_max_seconds_1h`
- `gpu_uuid_presence_flap_count_1h`
- `target_scrape_success_ratio_5m`
- `target_scrape_samples_ratio_5m`
- `target_scrape_duration_ratio_5m`

新增确定性规则：

- `gpu_reset_required > 0`：稳定性 critical，扣 40。
- 24 小时不可纠正 aggregate ECC 增量大于 0：显存 critical，扣 30。
- GPU 15 分钟平均利用率不低于 80% 且当前 PCIe 宽度小于最大宽度：互联 warning，扣 15。
- Correctable Row Remap 稳定累计值或 1 小时/24 小时单次低速新增：仅观察，不扣分。
- 1 小时新增不少于 2 行或 24 小时新增不少于 4 行：显存 attention，扣 8。
- 1 小时新增不少于 4 行或 24 小时新增不少于 8 行：显存 warning，扣 12。
- Uncorrectable ECC 新增和 Row Remap Failure 仍输出 critical；当前只记录、告警和支持人工处置，不执行任务排空、节点隔离、重启或诊断。

风扇特征先进入特征底座，不因单次高转速独立扣分，避免把正常散热响应误判为故障。

结构特征按现场 Prometheus 15 秒抓取周期，以每卡每小时 240 个样本为完整基线。除存在率和样本年龄外，v1.4 同时检查 1 小时最大间隔、UUID 存在性波动，以及 DCGM Target 5 分钟抓取成功率和样本量比。相对抓取耗时保留在快照与页面供审计，但因毫秒级基线易放大正常波动，不单独产生问题。结构异常不直接扣硬件健康分；恢复后对应问题自动清除。

## 4. 一致性容差

一致性校验只比较同一 15 分钟窗口的聚合特征及慢变化计数，避免把不同 scrape 时刻的瞬时利用率、功耗、显存占用误报为偏差。容差使用绝对值和相对值中的较大值：

- 15 分钟 GPU 最高温度：3°C；15 分钟显存最高温度：5°C。
- 15 分钟 GPU 平均利用率：10 个百分点。
- 15 分钟 SM 平均时钟：150MHz 或 10%。
- Row Remap 状态与计数：要求一致。

超过观察容差时仅记录双源数值差异，便于后续核对采样时间、窗口、单位和 exporter 实现。当前没有统一采样时间戳对齐和同窗口聚合证据，因此该差异不降低健康置信度、不生成 `gpu_source_inconsistency`、不进入问题统计或风险队列。历史试运行记录会自动清除活跃状态，并从问题列表、统计和训练数据中排除。

## 5. 验收要求

- DCGM 有值时始终选 DCGM。
- DCGM 缺失且 gpu_exporter 有等价值时完成回退，并降低置信度。
- 等价特征只产生一个规范值和一次规则判断。
- 双源数值差异仅作为快照审计信息；无论持续轮次均不扣硬件分、不降低置信度、不计入问题。
- gpu_exporter 独有规则必须有明确时间语义和负载保护。
- 每卡结构异常只进入数据质量统计，不重复计入硬件风险。
- 健康页顶部仅展示核心健康指标；每卡取值来源与回退信息保留在明细/API 中供审计。
