# ATLAS 事件链路与故障特征库实施方案 v1.0

> 文档状态：实施基线  
> 基线日期：2026-07-22  
> 适用模块：`incident`、`detection`、`prediction`、`degradation`  
> 用途：后续开发、验收、版本发布和阶段工作汇报

> 实施状态：`incident v0.2.1` 已于 2026-07-22 部署测试环境 `8077`；生产 `7077` 尚未升级。`8077` 的接收记录查询改为只读生产告警库，测试写入仍保持隔离。

## 1. 执行摘要

本轮核查形成两个明确结论：

1. 生产环境 `7077` 的 Alertmanager 告警接收链路仍在实时写入并持久化。页面长期显示 `100`，主要是前端固定请求最近 100 条、后端没有返回真实总数，并不等于数据库只有 100 条或链路停止。
2. 初版测试环境 `8077` 使用一次性数据库快照，导致生产新增告警不可见。现已修正为“测试写库 + 生产接收库只读查询”：页面可查看生产最新告警和完整历史，测试操作不能修改生产记录。
3. 故障预测前期应建立版本化的 **Atlas Fault Feature Catalog**。它不是简单的阈值表，而是将指标语义、适用硬件、时间窗口、缺失语义、检测/预测用途、基线、证据模板和版本统一管理的工程资产。
4. 第一阶段优先使用确定性故障特征、结构性可观测性特征、同机/同型号基线和风险排序。PyOD 负责异常分数；有足够维修和故障标签后，再训练并校准 1h/6h/24h 故障概率。
5. 性能衰减分为无侵入被动识别与维护窗口主动验证。不能把低利用率直接等同于算力衰减，也不能在无法确认 GPU 空闲时运行压力测试。

## 2. 事件数据的正式定义

ATLAS 必须在产品、API 和汇报中区分三层对象：

| 对象 | 数据来源 | 持久化对象 | 更新语义 | 主要用途 |
| --- | --- | --- | --- | --- |
| 接收记录 `ingestion` | Alertmanager/飞书兼容 Webhook | `alert_ingestion_records`，并保留原始 payload | 每次接收均新增记录 | 接入审计、解析核查、原始告警追溯 |
| 硬件事件 `fault_event` | Atlas 健康规则、XID/ECC/掉卡及后续日志事件化 | `gpu_fault_events` | 同一 GPU UUID + 规则在活动期内聚合，更新次数和最近时间 | 已发生/正在发生故障的检测和去重 |
| 故障案例 `incident/case` | 一个或多个相关硬件事件及人工反馈 | 规划中的案例/episode 数据模型 | 按 open、triaged、repairing、validating、closed 流转 | 处置、维修、验证、根因和训练标签闭环 |

因此，“接收记录数”“硬件事件数”和“未恢复故障数”不能互相替代。硬件事件行数不增加也不一定表示没有新信号：同一故障 episode 的重复命中会更新 `occurrence_count` 和 `last_observed_at`。

## 3. 2026-07-22 现场核查结论

### 3.1 生产环境 `7077`

- 已确认生产接口仍返回新接收记录。
- 核查时最新记录 ID 为 `1327`，接收时间为 `2026-07-22 13:49:25 +08:00`，来源为 `alertmanager`，内容为节点失活告警。
- 该结论证明接收和持久化链路仍在工作，但最新 ID 只说明记录已超过页面窗口，不等价于精确数据库总数。
- 生产 `7077` 当前仍是旧版服务，尚未提供测试环境中新开发的 `/api/v1/fault-events` 能力；生产升级前不得把两个环境的 API 能力混为一谈。

### 3.2 测试环境 `8077`

- 初版 `8077` 使用一次性复制的测试数据库，导致最新记录停留在 ID `1314`，这是测试数据源问题，不是生产接收链路停止。
- 修正后，`8077` 仍将 Webhook、测试分析和其他写入保存到 `/ops/atlas-test/atlas-test.db`，但接收记录查询通过 SQLite 只读连接访问 `/ops/atlas/atlas.db`。
- 只读连接不执行 AutoMigrate，不允许更新生产记录；因此可以验证生产最新告警、全量分页和筛选，又不会重复投递 Webhook 或影响生产处理状态。

### 3.3 “一直是 100 条”的直接原因

当前实现同时存在三个限制：

1. `web/src/App.tsx` 固定调用 `/api/v1/alerts/ingestions?limit=100`。
2. `pkg/storage/db.go` 的列表查询只支持 `limit`，最大 200，没有 `offset/cursor` 和总数查询。
3. `internal/gateway/handler.go` 返回的 `total` 是 `len(items)`，它只代表本次返回数量，不是数据库真实总数。

前端每 30 秒重新请求一次，但每次仍只取得最新 100 条，因此页面会持续显示 100。若新告警进入，列表头部会变化、尾部会滚出；缺少最新接收时间、接收速率和停更状态，使这种变化不容易被察觉。

## 4. Incident v0.2.1 改造范围

### 4.1 后端与数据库

- 列表 API 增加游标分页，优先采用 `before_id` 或 `(created_at,id)` 游标，避免大 offset 在长表上退化。
- 响应返回 `items`、`total`、`limit`、`next_cursor`、`has_more`、`latest_received_at`。
- 增加真实 `COUNT(*)`；后续数据量较大时允许用缓存统计，但必须标注统计时间。
- 接收状态增加 `last_received_at`、近 5m/1h 接收数、连续失败数和接入状态。
- 为 `alert_ingestion_records(created_at,id)`、来源、主机和处理状态建立与查询匹配的索引。
- 明确数据保留策略；删除或归档必须保留审计记录，首版不做自动删除。

建议响应契约：

```json
{
  "items": [],
  "total": 1327,
  "limit": 100,
  "next_cursor": "eyJiZWZvcmVfaWQiOjEyMjh9",
  "has_more": true,
  "latest_received_at": "2026-07-22T13:49:25+08:00",
  "source_mode": "local-live"
}
```

`total` 示例仅说明字段语义，不代表已经核验的精确生产总数。

### 4.2 前端交互

- 标题使用“接收记录”，不与“硬件事件”合并计数。
- 展示“当前 1–100 / 总 N 条”，提供上一页、下一页和页大小。
- 展示最新接收时间、5 分钟/1 小时接收速率和数据状态：`LIVE`、`STALE`、`SNAPSHOT`、`ERROR`。
- 当超过设定时间没有新记录时显示“暂无新告警”或“接入可能停更”，不得直接显示“系统健康”。
- 在测试环境固定显示环境标识、数据库来源和快照时间。
- 新记录到达时保留当前筛选和详情选择；用户位于非第一页时提示有新记录，不强制跳页。
- 原始告警、解析结果、AI 分析状态和关联硬件事件分别展示。

### 4.3 测试环境数据策略

实施策略是保持 `8077` 独立写库，同时为接收记录配置生产 SQLite 只读连接。由于新旧服务位于同一监控节点，可直接获得真实总数和游标分页，无需重复投递 Webhook，也不受生产旧 API 最近 100 条窗口限制。

任何跨环境读取必须满足：

- 生产只读，不修改生产记录状态
- 测试环境不触发生产通知或回调
- UI 明确展示数据来源
- 失败时回退到本地快照并标记，不静默混合两种来源

### 4.4 验收标准

- 页面总数与数据库真实总数一致。
- 可以浏览超过 200 条的历史记录，分页过程无重复、无遗漏。
- 新告警进入后，最新时间、速率和第一页在 30 秒轮询窗口内更新。
- `8077` 明确展示 `UPSTREAM-READONLY`，不会被误认为独立接收或可写生产数据源。
- 接收记录、硬件事件和故障案例的数量及状态语义在 API 与 UI 中一致。
- 事件链路停更、解析失败和查询失败能分别识别。

## 5. Atlas Fault Feature Catalog v1

### 5.1 定位

故障特征库是规则评分、异常检测、风险排序、监督预测、性能衰减和 AI 报告的共同底座。它解决以下问题：

- 同一个 DCGM/Prometheus 指标在不同型号上是否支持、如何解释
- 指标缺失是硬件异常、采集异常、型号不支持还是尚未建立基线
- 某个特征用于检测当前故障、预测未来风险，还是只用于解释
- 某条结论来自固定规则、同类基线、统计模型还是主动验证
- 特征、阈值和模型更新后，历史结果能否按版本回放

市场上没有可直接覆盖 4090、H100、H200、现有 exporter 和本地运维流程的通用成品特征库。ATLAS 应吸收官方字段语义、开源诊断规则和论文特征设计，建立自己的版本化注册表。

### 5.2 注册表字段

```text
feature_id
feature_version
domain
display_name_zh
display_name_en
source_type
source_metric_or_event
hardware_scope
supported_models
driver_dcgm_constraints
window
transform
precondition
missing_semantics
risk_direction
purpose
baseline_scope
threshold_source
evidence_template
owner
status
created_at
updated_at
```

关键枚举：

- `purpose`: `health` / `detection` / `anomaly` / `prediction` / `degradation` / `explanation`
- `missing_semantics`: `not_supported` / `not_collected` / `target_down` / `gpu_missing` / `insufficient_history` / `unknown`
- `threshold_source`: `vendor` / `deterministic_rule` / `peer_baseline` / `historical_baseline` / `model` / `maintenance_benchmark`
- `status`: `draft` / `shadow` / `active` / `deprecated`

特征值为零和特征不受支持必须严格区分。尤其是 RTX 4090 不具备 H100/H200 等价的 ECC、Row Remap 和部分诊断能力，缺失不能折算成零风险。

### 5.3 首批特征域

| 特征域 | 首批特征 | 主要用途 | 预警潜力 |
| --- | --- | --- | --- |
| 可用性/结构 | GPU 数量变化、UUID 消失、指标消失、Target 状态、scrape 样本数下降、抓取失败/间隔异常 | 掉卡、不可用、采集失效检测；掉卡前结构性风险 | 检测强，部分场景可提前预警 |
| XID/ECC/显存 | XID 类别和频次、DBE、SBE delta/burst、Row Remap、Retired Pages、Reset Required | 显存故障检测与风险排序 | H100/H200 中等；4090 能力受限 |
| PCIe/NVLink | Replay rate、协商 Gen/Width 下降、AER、CRC、NVLink 带宽/错误、Fabric 状态 | 链路退化、掉卡候选、通信衰减 | 中等 |
| 温度/供电/时钟 | 温度斜率、同机温差、时钟波动、Throttle duty、功耗效率、BMC 风扇/PSU/SEL | 散热和供电退化、降频 | 中到强 |
| 性能衰减 | Effective clock ratio、显存带宽比、PCIe/NVLink 带宽比、同机 8 卡离群值 | 灰色故障和 fail-slow 识别 | 趋势可识别，不直接等价于失效概率 |
| 历史/维修 | 历史故障次数、复发间隔、维修/更换、驱动版本、服役时间、标签置信度 | 风险排序、监督模型和解释 | 标签成熟后增强 |

每个原始特征至少派生 5m、1h、6h、24h 窗口中的适用统计：均值、最大值、P95、标准差、delta/rate、斜率、burst 次数、缺失比例、同机/同型号分位数。7d/30d 用于历史基线和离线训练，不作为高频在线查询直接扫原始大表。

### 5.4 可观测性结构特征

“数值指标没有异常”不代表故障没有前兆。部分 GPU detachment 在故障前最有价值的信号可能是指标序列消失、scrape 样本数下降、抓取耗时/失败变化和连续 gap。ATLAS 将这类结构特征与温度、电压、ECC 等数值特征同等纳入风险排序。

首批结构特征：

```text
gpu_series_present_ratio_5m
gpu_metric_family_count_delta_5m
target_scrape_success_ratio_5m
target_scrape_samples_ratio_5m
target_scrape_duration_ratio_5m
gpu_uuid_presence_flap_count_1h
gpu_count_delta_5m
metric_gap_max_seconds_1h
```

### 5.5 性能衰减特征

被动检测不占用 GPU，适合持续运行：

- `effective_clock / expected_clock_under_comparable_load`
- P-state、power/SM efficiency、利用率条件化温度
- thermal/power throttle duty
- PCIe 协商宽度/代际与 replay 增量
- 同机 8 卡、同型号群体的稳健 Z-score 和分位数
- NVLink/NCCL 指标存在时的 peer deviation

主动验证会占用硬件，只能在人工维护窗口执行：

- GEMM/矩阵计算
- 显存带宽和显存错误检查
- PCIe H2D/D2H/P2P
- NVBandwidth、NCCL bus bandwidth
- DCGM targeted diagnostics 和 SuperBench 精简测试集

主动结果必须绑定 `benchmark_version`、软件栈、功耗/时钟设置、重复次数、对照群体和波动区间。不得使用统一的“低于标称 80%”作为所有型号、所有负载的生产阈值。

## 6. 特征库到模型的实现路径

```text
官方字段/日志/监控结构/维修反馈
    -> Feature Catalog 注册与能力矩阵
    -> 5m/1h/6h/24h 特征物化
    -> 确定性规则与健康评分
    -> 同机/同型号风险排序
    -> PyOD anomaly_score（shadow）
    -> 人工确认故障与维修标签
    -> 监督模型 failure_probability_1h/6h/24h
```

工程约束：

1. `health_score`、`anomaly_score`、`failure_probability` 分开输出。
2. 4090、H100、H200 按能力矩阵和型号分组，不训练一个通用模型。
3. PyOD 首批采用 ECOD、IForest、COPOD、PCA、HBOS，先建立简单、可复现的基线。
4. 没有足够独立故障案例时，不宣称模型可以输出可信故障概率。
5. 对突发 DBE、GPU Lost 等难以精确预测的故障，优先输出高风险队列和 Top-K 维护对象，而不是承诺具体故障时刻。
6. 所有模型先 shadow 运行，用每 1000 GPU·天误报数、Top-K 命中、提前量和运维确认成本验收。

## 7. 可借鉴项目与产品

### 7.1 直接采用或优先集成

| 项目 | 可借鉴能力 | ATLAS 决策 |
| --- | --- | --- |
| [NVIDIA DCGM Field IDs](https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/dcgm-api-field-ids.html) | 官方字段 ID、单位、范围和支持语义 | 作为特征注册和能力矩阵的一手依据 |
| [NVIDIA DCGM Diagnostics](https://docs.nvidia.com/datacenter/dcgm/latest/user-guide/dcgm-diagnostics.html) | GPU 诊断与测试等级 | 仅维护窗口、人工确认无任务后使用侵入式项 |
| [PyOD](https://github.com/yzhao062/pyod) | 无监督异常检测算法 | 作为 Phase 3 算法底座，不承担遥测接入和事件闭环 |
| [Microsoft SuperBench](https://microsoft.github.io/superbenchmark/docs/introduction/) | 标准化 benchmark、结果和基线 | 采用精简测试与结果契约，不照搬大规模调度系统 |
| [SuperBench Data Diagnosis](https://microsoft.github.io/superbenchmark/docs/user-tutorial/data-diagnosis/) | YAML 基线规则、异常验证 | 用于性能衰减基线和维修复测参考 |

### 7.2 架构和规则参考

| 项目/研究 | 可借鉴能力 | ATLAS 决策 |
| --- | --- | --- |
| [NVIDIA NVSentinel](https://github.com/NVIDIA/NVSentinel) | DCGM/journalctl 健康监控、事件持久化、分析、隔离和修复流程 | 借鉴故障分类、事件模型和处置状态；当前环境不由 ATLAS 负责调度，且不假定 Kubernetes，不直接部署完整栈 |
| [Production GPU Failure Prediction](https://arxiv.org/abs/2201.11853) | 温度、功耗、利用率、机型、驱动、年龄等特征；滑窗、集成和重训 | 作为监督预测特征和评估基线，按现场数据复现 |
| [Observability-aware Early Warning](https://arxiv.org/abs/2603.28781) | 指标缺失、scrape 样本与 gap 等结构性前兆 | 纳入数据质量和风险排序；配套公开数据 DOI `10.5281/zenodo.19052367` 可用于离线验证 |
| [HeaRank](https://arxiv.org/abs/2607.15115) | 难预测故障采用风险排序和 Top-K 运维队列 | 近期研究，仅作为候选思路；必须在 ATLAS 现场回放后才可引用效果 |
| [ARGUS](https://arxiv.org/abs/2606.20374) | 大规模 GPU fail-slow/straggler 诊断 | 借鉴性能域拆分和群体对比；未确认有可直接采用的开源实现 |

### 7.3 商业产品参考

| 产品 | 可借鉴能力 | 适用判断 |
| --- | --- | --- |
| [NVIDIA Mission Control](https://www.nvidia.com/en-gb/data-center/mission-control/) | 健康检查、性能验证、自动恢复和机群运维 | 适合 DGX/企业 NVIDIA 栈评估；ATLAS 参考其产品闭环，不把商业许可能力写成自研已交付 |
| [NVIDIA UFM](https://www.nvidia.com/en-us/networking/infiniband/ufm/) | InfiniBand fabric 遥测、分析和预测性维护 | 作为后续网络硬件可靠性模块参考 |

没有经过源代码、官方文档或论文核验的小型 GitHub 演示项目，只进入调研候选，不进入生产依赖清单。

## 8. 版本与交付计划

| 迭代 | 交付物 | 交付后汇报口径 | 验收 |
| --- | --- | --- | --- |
| `incident v0.2.1` | 真实总数、游标分页、新鲜度、速率、环境/数据源标识 | 已定位“固定 100 条”是展示/API 统计问题，并完成事件链路可观测改造 | 可浏览全量；新告警可见；快照与实时不混淆 |
| `feature-catalog v1.0.0` | 注册/读取 API、能力矩阵、20 个健康核心特征和 snapshot 版本 manifest | 健康评分已消费统一、可版本化的特征底座；PyOD、风险排序和监督预测可复用同一契约 | 特征支持、时间/缺失语义和血缘可查询、可回放 |
| `feature-catalog v1.2.0` / `gpu-health v1.2.0` | 27 个规范特征、45 个 DCGM/gpu_exporter 源查询、Correctable Row Remap 1h/24h 增量、单位规范化、DCGM 优先回退与来源审计 | 稳定 Correctable 累计值不扣分，只有快速增长进入风险；Uncorrectable/Remap Failure 只告警，不自动操作任务或节点 | 逐特征来源可审计，回退降低置信度，稳定累计值不产生风险事件 |
| `feature-catalog v1.3.0` / `gpu-health v1.3.0` / `quality v0.5.0` | 30 个规范特征、48 个源查询；每卡 1h 样本数、存在率和当前样本年龄 | 结构异常进入数据质量问题而不误判为硬件故障；恢复后自动清除 | 15 秒现场基线下完整率和样本年龄可查，连续性页面/API/问题生命周期一致 |
| `feature-catalog v1.4.0` / `gpu-health v1.4.1` / `quality v0.6.1` | 35 个规范特征、53 个源查询；最大 gap、UUID flap 和三项 DCGM Target 抓取质量特征 | GPU 序列与采集链路联合判定；相对耗时仅审计，避免微小绝对值被比例放大 | 720 GPU 查询覆盖；连续性明细、摘要和数据问题证据一致，无耗时比误报 |
| `feature-catalog v1.5.0` / `quality v0.8.0` / `data-statistics v0.4.0` | metric-family 单节点 shadow canary；数据质量按分类输出历史、已解决、遗留和当前检测统计 | canary 不进入健康评分且不自动发布；数据质量台账由独立“问题统计”子页承载 | canary 查询仅返回 8 GPU；规则文件具备范围测试；数据质量侧边栏、独立子页与问题列表筛选一致 |
| `degradation v0.1.0` | 被动衰减候选、同机/同型号对比、证据输出 | 无侵入识别可用但变慢的 GPU 候选 | 不把空闲误报为衰减，输出基线与置信度 |
| `degradation v0.2.0` | 维护窗口主动验证与维修复测 | 建立算力/显存/PCIe/NVLink 分类验证 | 测试可重复且不影响生产任务 |
| `prediction v0.1.0` | PyOD shadow、风险排序、历史回放 | 输出异常与高风险队列，不冒充故障概率 | 有误报率、Top-K 命中和提前量报告 |
| `prediction v1.0.0` | 有监督概率模型和校准 | 标签成熟后提供 1h/6h/24h 故障概率 | 时间/UUID 隔离验证、概率校准、可回滚 |

## 9. 工作汇报模板

阶段汇报统一回答五个问题：

1. **覆盖了什么**：节点、GPU 型号、指标、事件源和有效时间范围。
2. **发现了什么**：硬件问题、监控数据问题、事件接入问题分别统计。
3. **交付了什么**：模块版本、页面/API、规则/特征数量、验证结果。
4. **能做到什么程度**：检测、风险排序、异常分数、概率预测分别说明，不夸大提前量。
5. **下一步和依赖**：需要的日志、维修反馈、维护窗口、资产接口和数据积累。

推荐量化指标：

- 接收链路最新时间、5m/1h 接收数、解析成功率、积压和失败数
- GPU/节点/Target 覆盖率、指标缺失率、身份冲突和陈旧数据数
- 故障检测时延、重复聚合率、未恢复事件和已关闭案例数
- 每 1000 GPU·天误报数、Top-K 命中率、中位/P10/P90 提前量
- 性能衰减候选数、主动验证数、确认数、维修后复测通过率
- 人工根因标签覆盖率和可用于训练的独立故障案例数

汇报中禁止把页面返回的 100 条称为数据库总数，禁止把异常分数称为故障概率，禁止把未在现场复现的论文或商业产品指标作为 ATLAS 承诺。

## 10. 决策记录

| 日期 | 决策 | 原因 |
| --- | --- | --- |
| 2026-07-22 | 接收记录、硬件事件和故障案例分层建模 | 三者来源、更新和处置语义不同 |
| 2026-07-22 | `8077` 写入使用测试库，接收记录查询使用生产只读库 | 修正一次性快照滞后，同时避免跨环境写入和重复 Webhook 副作用 |
| 2026-07-22 | 建立 Atlas 自有版本化 Fault Feature Catalog | 通用算法库不提供现场遥测语义、型号能力和运维闭环 |
| 2026-07-22 | 突发故障优先风险排序，不承诺精确故障时刻 | 部分 DBE/GPU Lost 没有稳定数值前兆 |
| 2026-07-22 | 性能衰减采用被动识别 + 维护窗口主动验证 | 兼顾在线安全和可重复确认 |
| 2026-07-22 | `incident v0.2.1` 在测试环境交付 | 修复固定显示 100 条和测试快照被误认为实时流的问题；生产升级需单独验收 |
