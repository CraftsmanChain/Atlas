# Atlas Feature Catalog v1

> 当前契约版本：`v1.5.0`（2026-07-24）

Feature Catalog 是健康评分、PyOD 异常检测、风险排序、监督预测和性能衰减检测共享的数据契约。目录版本为 `1.0.0`；特征定义按 `(name, version)` 不可重复注册。

## 数据契约

每项特征必须声明：

- `name`、`version`、中英文名称和 `domain`
- `entity_type`、`granularity` 和 `supported_models`
- `source_type`、`source_reference`、`computation` 和 `lineage`
- `time_semantics`、`window`、`freshness_sla_seconds`
- `missing_strategy`、`quality_status`、`status`
- `purposes` 和 `owner`

首批 `purposes` 覆盖 `health`、`anomaly`、`risk_ranking`、`prediction`、`degradation`。缺失值不能自动折算为零；当前通用策略为 `unknown_not_zero`，Row Remap 等型号受限指标使用 `not_supported_or_not_collected`。

## API

- `GET /api/v1/features`：列出定义；支持 `domain`、`status`、`purpose` 筛选。
- `GET /api/v1/features/{name}?version=1.0.0`：读取指定版本；不传版本时返回最近注册版本。
- `POST /api/v1/features`：注册完整定义；重复 `(name, version)` 返回 `409`，契约不完整返回 `400`。

生产部署应在网关层限制注册接口，只向平台管理员或发布流水线开放。读取接口可供规则、离线训练和模型服务使用。

## 已接入消费方

健康评分服务不再维护独立的 Prometheus 特征清单。Catalog v1.5 注册 36 个规范定义：35 个健康特征生成 53 个 DCGM/gpu_exporter/Prometheus 在线源查询；`gpu_metric_family_count_delta_5m` 作为第 36 个 shadow recording-rule 特征，只服务异常检测、风险排序和预测准备。

`correctable_remapped_rows_delta_1h` 与 `correctable_remapped_rows_delta_24h` 将 Correctable Row Remap 的累计值转为近期增量。稳定累计值和单次低速新增只作为观察证据，不降低健康分；只有达到规则版本声明的增长门槛才进入风险评分。

结构特征以现场 15 秒 DCGM 采样周期为基线：

- `gpu_metric_samples_1h`：每卡最近 1 小时样本数，完整窗口期望 240。
- `gpu_metric_presence_ratio_1h`：每卡最近 1 小时存在率，上限 100%。
- `gpu_metric_sample_age_seconds`：每卡当前最新样本年龄。
- `gpu_metric_gap_max_seconds_1h`：最近 1 小时相邻样本最大间隔。
- `gpu_uuid_presence_flap_count_1h`：最近 1 小时 UUID 序列存在性变化次数。
- `target_scrape_success_ratio_5m`：DCGM Target 最近 5 分钟抓取成功率。
- `target_scrape_samples_ratio_5m`：DCGM Target 5 分钟样本量相对 1 小时基线的比例。
- `target_scrape_duration_ratio_5m`：DCGM Target 5 分钟平均抓取耗时相对 1 小时基线的比例。

存在率、年龄、gap、flap、Target 抓取成功率和样本量比按两级阈值进入数据质量问题；例如 gap 超过 45 秒进入 attention、超过 120 秒进入 warning，UUID flap 达 1 次进入 attention、达到 2 次进入 warning。相对抓取耗时容易受毫秒级基线放大影响，当前只作审计，待有绝对耗时门槛后再参与判定。它们不扣硬件健康分，也不产生硬件故障事件。

语义等价指标按 DCGM 主源、gpu_exporter 降级备用合并，在查询层完成比例、Hz 和 bytes 的单位规范化；snapshot 记录逐特征来源、回退数量和双源偏差。4090 不会把不支持的显存温度或 Row Remap 当成缺失风险；H100/H200 会纳入 Row Remap 与详细 ECC 能力。完整契约见 [GPU 双源健康评分契约 v1.3](gpu-dual-source-health-v1.1.md)。

内置定义在服务启动时幂等注册，历史定义不会被覆盖。健康特征 snapshot 已写入 `feature_catalog_version` 和逐特征 `feature_versions` manifest；后续模型数据集和推理结果也必须携带同类版本集合，才能支持严格回放。

## 下一批接入

1. 由 Prometheus 管理员发布单节点 metric-family canary，观察 24 小时规则耗时、序列数和非零 delta 验证。
2. 性能衰减：有效时钟比、同机/同型号稳健分位数、链路带宽比和 throttle duty。
3. 历史与标签：复发间隔、维修/更换、服役时间、驱动版本和标签置信度。
4. 将 snapshot 的特征版本 manifest 延伸到 PyOD shadow 结果和监督训练数据导出。
