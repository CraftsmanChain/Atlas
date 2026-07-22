# Atlas Phase 0：数据正确性与覆盖治理实施计划

> 建议周期：10 个工作日
>
> 目标环境：`10.111.201.1:8077` 测试实例
>
> 生产约束：只读访问 Prometheus、GPU 节点和 BMC；不重启 exporter，不运行 GPU 压测；不接入或操作调度系统

## 1. 阶段目标

Phase 0 不计算健康分，不训练异常模型，也不做故障概率预测。本阶段只交付一个可信的数据底座和可操作的数据质量工作台。

平台不要求能够登录所有节点。对于只有 Prometheus 等监控数据读取权限的集群，Phase 0 仍必须通过 Target、指标连续性、标签/身份一致性和资产对账发现监控覆盖与数据异常。Atlas 输出问题和核查依据，但不自动修复 exporter、监控配置或源数据；节点、Agent 与 BMC 的只读访问属于可选证据增强。

完成后必须能准确回答：

1. 当前应管理多少 GPU 节点、多少物理 GPU 槽位、多少 UUID。
2. 每个 GPU UUID 属于哪台主机、哪个卡号和哪个 PCI Bus ID。
3. 节点 DOWN、exporter DOWN、target 缺失、指标不支持和数据过期分别是什么状态。
4. 哪些 GPU/节点数据完整，哪些缺字段，缺失原因是什么。
5. 4090、H100、H200 分别支持哪些指标，哪些值是无效值或 DCGM 哨兵值。
6. 页面展示的数据来自哪个同步批次，最后成功时间是什么。

## 2. 当前现场基线

以 2026-07-16 监控节点 Prometheus 核查结果为基线：

| 项目 | 当前值 |
|---|---:|
| GPU 节点 | 90 |
| 预期 GPU 槽位 | 720 |
| 已确认唯一 UUID | 720 |
| 实时 DCGM UUID | 704 |
| 历史恢复 UUID | 16 |
| UUID 未知 | 0 |
| 节点正常 | 89 |
| 节点可达但 target 缺失 | 1（`10.114.4.25`） |
| 节点 DOWN | 0 |

关键 target 状态：

| 组件 | UP | DOWN | MISSING |
|---|---:|---:|---:|
| DCGM | 88 | 1 | 1 |
| GPU Exporter | 78 | 11 | 1 |
| Node Exporter | 89 | 0 | 1 |
| IPMI | 90 | 0 | 0 |

资产范围以资产文档为准：主机名包含 `gpu`（不区分大小写）才进入 GPU 资产域。`10.114.4.37` 没有匹配的 GPU 主机名，作为非 GPU/待确认监控目标保留在覆盖核查中，不进入 GPU 节点、槽位和 UUID 统计。

Phase 0 的首个启动快照为：

- `docs/assets/gpu-node-inventory-2026-07-16.csv`
- `docs/assets/gpu-uuid-inventory-2026-07-16.csv`
- `docs/assets/gpu-target-down-report-2026-07-16.md`

这三个文件只用于首批导入、开发回放和验收对照，不是长期资产真值。资产库必须定期从监控系统重新发现和对账，并保留新增、替换、离线和下架历史。

## 3. 产品范围

### 3.1 必须交付

- 可持续同步的 GPU 节点、GPU 槽位和 UUID 资产库
- 节点/GPU 新增、替换、离线、恢复和下架生命周期
- 每次对账的资产差异和变更事件
- DCGM/GPU/Node/IPMI target 状态同步与分类
- 节点状态与采集组件状态分离
- 数据新鲜度、缺失、哨兵值和能力支持状态
- 4090/H100/H200 指标能力矩阵
- 数据质量问题清单和处理状态
- 资产、target、能力矩阵和问题清单 API
- 前端资产页、数据质量页、节点/GPU 详情抽屉、全局搜索和 CSV 导出
- 同步批次、来源和最后成功时间展示

### 3.2 明确不做

- 健康分、异常分和故障概率
- XID 内核日志事件化
- 故障案例状态机和维修闭环
- 主动 benchmark、DCGM diagnostics、reset、reboot、drain
- 浏览器直接执行任意 PromQL
- 大模型诊断或自动操作
- 自动修复 DOWN exporter

### 3.3 时间盒优先级

为保证 1～2 周可交付，按以下优先级执行：

| 优先级 | 内容 | 是否阻塞 Phase 0 退出 |
|---|---|---|
| P0-MUST | 监控发现资产、首批 90 节点/720 槽位导入、持续对账、UUID 映射、四类 target 分类、节点抑制、清洗规则、数据库、只读 API、资产页和数据质量页 | 是 |
| P0-SHOULD | 能力矩阵、历史 UUID 恢复、CSV 导出、同步历史、全局搜索 | 是，但可压缩展示形式 |
| P0-STRETCH | 人工 ACK/负责人/备注、完整 BMC 槽位映射、同步批次差异对比 UI | 否；未完成转入 Phase 0.5 |

不得为了完成 STRETCH 项压缩身份正确性、状态分类和无效值测试。

## 4. 状态模型

### 4.1 节点状态

```text
UP           Node Exporter 正常，节点数据新鲜
REACHABLE    ICMP/BMC 可达，但 Node target 缺失或异常
DOWN         主机网络不可达，并有 Node/采集链路缺失佐证
UNKNOWN      证据不足，不能确认 UP 或 DOWN
MAINTENANCE  由硬件负责人手工确认；Phase 0 预留
```

节点 DOWN 不能仅由某个 exporter DOWN 推导。`connection refused` 代表目标主机拒绝该端口连接，不等于整机 DOWN。

### 4.2 Target 状态

```text
UP       Prometheus active target 抓取成功
DOWN     active target 存在，但抓取失败
MISSING  资产应存在该 target，但 active targets 中没有
STALE    最近成功样本超过约定时限
UNKNOWN  无足够信息
```

每个状态同时保存：

- `health`
- `reason_code`
- `last_error`
- `last_scrape_at`
- `last_success_at`
- `scrape_url`
- `source_snapshot_id`
- `suppressed`
- `suppression_reason`

节点确认 DOWN 时，组件 DOWN 仍保存原始状态，但前端归入“因节点 DOWN 抑制”，不进入待确认组件数量。

首批确定性原因码：`node_inband_unreachable`、`target_missing`、`connection_refused`、`scrape_timeout`、`network_unreachable`、`dns_error`、`tls_error`、`scrape_error`、`exporter_down`。节点的 Node/DCGM/GPU Target 均不可用时，DCGM/GPU 派生问题标记为 `node_inband_unreachable` 并抑制；这只表示带内采集链路不可达，不直接等同整机掉电。IPMI/BMC 保持独立判断。

### 4.3 GPU 身份状态

```text
ACTIVE          当前指标中存在 UUID
HISTORY_ONLY    当前 target 异常，从历史数据恢复 UUID
UUID_UNKNOWN    已知物理槽位，但 UUID 未知
CONFLICT        UUID、节点、卡号或 PCI Bus ID 关系冲突
REPLACED        同一槽位出现新 UUID，旧 UUID 保留为历史资产
```

GPU index 只表示槽位，不能作为资产主键。数据库使用内部 `asset_id`，UUID 允许在 `UUID_UNKNOWN` 状态为空；非空 UUID 必须全局唯一。

### 4.4 指标能力状态

```text
SUPPORTED
UNSUPPORTED
TEMPORARILY_MISSING
INVALID
UNKNOWN
```

`UNSUPPORTED` 不算采集故障；`TEMPORARILY_MISSING` 和 `INVALID` 进入数据质量问题。

## 5. 后端功能与能力

### 5.1 Prometheus 只读客户端

新增 `internal/prometheus`：

- 固定 Prometheus 地址和请求超时
- 只允许代码内注册的查询模板
- 禁止 API 接收任意 PromQL
- 限制并发数、返回序列数和时间范围
- 记录查询耗时、错误和返回序列数
- target 查询与指标查询分别设置缓存
- 长周期历史查询只用于资产恢复，不进入页面实时请求

建议同步频率：

| 数据 | 周期 | 超时 |
|---|---:|---:|
| active targets | 600 秒 | 10 秒 |
| 当前 GPU 身份指标 | 1800 秒（30 分钟） | 30 秒 |
| 能力矩阵统计 | 每日 | 2 分钟 |
| 历史 UUID 恢复 | 手动/每日 | 2 分钟 |

页面只查询 Atlas 数据库，不在每次打开页面时请求 Prometheus。

### 5.2 资产同步器

新增 `internal/inventory`，执行顺序：

```text
读取 active targets
  -> 构建 GPU 节点候选集合
  -> 读取当前 DCGM 身份标签
  -> 规范化 host/UUID/BDF/model/index
  -> 补齐已知物理槽位
  -> 对异常 target 使用历史 UUID
  -> 比较上一批快照
  -> 生成冲突与缺失问题
  -> 单事务写入资产和同步批次
```

同步必须幂等；同一输入重复执行不得创建重复 GPU。失败批次不覆盖最后一次成功快照。

首批资产从当前监控可获得的数据建立，发现源包括：

1. Prometheus active targets 与 scrape labels
2. 当前 DCGM/GPU Exporter 身份标签
3. IPMI target 与 BMC 地址
4. Prometheus 保留期内的历史 GPU UUID
5. 硬件负责人确认的上下架、替换和维修反馈

Atlas 不把任意一次 Prometheus 即时查询结果直接当作完整资产库。当前观测用于发现和更新；Atlas 保存上一状态和生命周期，避免 target 暂时消失后资产被物理删除。

#### 5.2.1 资产真值与来源优先级

```text
监控配置/targets       -> 发现应监控的节点和组件
实时 DCGM/NVML        -> 确认当前 GPU UUID、槽位、型号和 PCI BDF
历史 Prometheus       -> 恢复当前离线节点的已知身份
BMC/主机序列号        -> 确认物理服务器身份
维修/上下架确认        -> 确认替换、退役和生命周期终态
```

每个资产字段保存 `source`、`source_priority`、`observed_at` 和 `sync_run_id`。高优先级来源覆盖低优先级来源时必须生成字段变更记录，不能静默覆盖。

#### 5.2.2 节点生命周期

```text
DISCOVERED -> ACTIVE -> DEGRADED/OFFLINE -> ACTIVE
                         |
                         -> RETIRING -> RETIRED
```

- `DISCOVERED`：监控首次发现，尚未满足稳定确认条件
- `ACTIVE`：连续两个同步周期存在，且至少一个主身份来源有效
- `DEGRADED`：节点可达但关键 target 缺失或异常
- `OFFLINE`：节点确认不可达；资产仍保留
- `RETIRING`：监控中持续缺失，等待硬件负责人确认下架
- `RETIRED`：确认下架；默认搜索不展示，但历史和事件仍可查询

不得仅因一个同步周期没有数据就标记 `RETIRED`。建议：连续 `3` 次失败进入 DEGRADED/OFFLINE，连续 `24h` 未出现进入 RETIRING；RETIRED 需要人工或权威系统确认。

#### 5.2.3 GPU 替换识别

同一 `(node_identity, gpu_index/pci_bus_id)` 出现新 UUID 时：

1. 新 UUID 创建为新资产并标记 `DISCOVERED`。
2. 旧 UUID 标记 `REPLACED`，保存 `replaced_by_uuid` 和最后出现时间。
3. 创建 `GPU_REPLACED_CANDIDATE` 变更事件。
4. 若节点序列号同时变化，则优先识别为节点替换，不直接判单卡替换。
5. 未获得维修反馈前显示“待确认替换”，不得删除旧 UUID。

`gpu_index` 和 IP 地址都可能复用。节点长期身份优先使用主机序列号/BMC 身份；GPU 长期身份使用 UUID。

#### 5.2.4 资产变更事件

每次同步比较上一成功快照，至少生成：

```text
NODE_DISCOVERED
NODE_OFFLINE
NODE_RECOVERED
NODE_REPLACED_CANDIDATE
NODE_RETIRING
NODE_RETIRED
GPU_DISCOVERED
GPU_MISSING
GPU_RECOVERED
GPU_REPLACED_CANDIDATE
GPU_IDENTITY_CHANGED
TARGET_ADDED
TARGET_REMOVED
TARGET_STATE_CHANGED
```

事件保存变更前值、变更后值、来源、首次/最近观察时间、确认状态和关联维修记录。

#### 5.2.5 同步周期

| 对账任务 | 周期 | 说明 |
|---|---:|---|
| target 状态 | 600 秒 | 状态更新，不改资产终态 |
| GPU/节点身份增量对账 | 1800 秒（30 分钟） | 发现新增、离线、恢复和 UUID 变化 |
| 全量资产对账 | 每日 | 与 targets、BMC、历史身份完整比对 |
| 历史 UUID 恢复 | 每日 | 仅处理当前缺失/离线节点 |
| RETIRING/RETIRED 审核 | 每日/人工 | 不自动物理删除 |

所有任务保存同步水位、运行耗时、变更数量和错误；同一任务禁止并发执行。

实现约定：

- `target_status` 只读取 Prometheus active targets，更新 Target 矩阵和节点可达状态，不查询 GPU 指标。
- `identity_incremental` 读取当前 GPU 身份指标，发现新增、恢复、UUID/状态变化，不执行历史查询和自动下架。
- `full_reconcile` 读取当前数据并仅为 DCGM 不可用节点恢复历史 UUID，同时执行资产下架判断。
- 三类任务串行执行，避免 SQLite 写冲突和同一观测窗口产生矛盾变更。
- `inventory_sync_runs.task_type` 保存任务类型；资产变更通过 `sync_run_id` 关联到产生该变更的同步批次。

### 5.3 字段规范化与校验

统一规则：

| 字段 | 规范 |
|---|---|
| GPU UUID | 大写前缀统一为 `GPU-`，去除首尾空白，全局唯一 |
| PCI Bus ID | 统一 `dddd:bb:ss.f`，接受 DCGM 的 8 位 domain 输入 |
| GPU index | 整数 `0～7`，超出范围生成问题 |
| host IP | 标准 IPv4，GPU 管理网为 `10.114.4.0/24` |
| BMC IP | 独立字段，不与 host IP 混用 |
| model | 标准值 `RTX4090/H100/H200`，保留原始 `model_name` |
| `sn` | Phase 0 标记为 `host_serial_candidate`，未确认前不叫 GPU 序列号 |

冲突规则：

- 同一 UUID 同时关联多个节点：critical
- 同一节点/卡号同时存在多个 ACTIVE UUID：critical
- 同一节点/PCI Bus ID 同时关联多个 ACTIVE UUID：critical
- 预期 8 卡但少于 8 个槽位：warning
- UUID 仅历史可见：info/warning，取决于节点状态

### 5.4 DCGM 数据清洗器

新增统一值状态，不把无效值转成 0：

```json
{
  "value": null,
  "value_state": "UNSUPPORTED",
  "raw_value": "9223372036854775794",
  "reason": "DCGM_BLANK_SENTINEL"
}
```

必须覆盖：

- DCGM blank/not-supported 哨兵值
- `NaN`、`+Inf`、`-Inf`
- 明显违反物理范围的温度、功耗、利用率和链路值
- counter 与 gauge 类型区分
- 不支持值不进入后续评分输入

清洗规则需要表驱动和单元测试，禁止散落在 API handler 中。

### 5.5 指标能力矩阵

按 `model + metric_name` 维护：

- 指标语义和单位
- DCGM field ID
- 数据类型：gauge/counter/state
- 能力状态
- 有效样本比例
- 首次/最后有效时间
- 无效原因分布
- 后续健康维度归属

Phase 0 首批至少覆盖：温度、功耗、SM/显存利用率、时钟、降频原因、XID、ECC、SRAM、row remap、PCIe、NVLink 和 reset 状态。

### 5.6 数据质量问题引擎

自动生成问题码：

```text
NODE_UNREACHABLE
TARGET_DOWN
TARGET_MISSING
TARGET_STALE
GPU_UUID_UNKNOWN
GPU_UUID_CONFLICT
GPU_SLOT_COUNT_MISMATCH
PCI_BUS_ID_CONFLICT
METRIC_TEMPORARILY_MISSING
METRIC_INVALID_SENTINEL
MODEL_CAPABILITY_UNKNOWN
BMC_MAPPING_MISSING
```

问题字段：`issue_id/code/object_type/object_id/severity/status/first_seen_at/last_seen_at/details/owner/comment/resolved_at`。

Phase 0 MUST 支持 `OPEN/AUTO_RESOLVED` 自动状态；`ACKNOWLEDGED/RESOLVED/IGNORED`、负责人和备注属于 STRETCH。若启用人工写操作，必须先有操作员认证并写审计记录，且不得触发远程操作。

### 5.7 数据库表

SQLite 足以支撑 Phase 0 的 90 节点和 720 槽位，沿用 GORM；高频时序仍保留在 Prometheus。

建议新增：

| 表 | 用途 |
|---|---|
| `inventory_sync_runs` | 同步批次、来源、耗时、状态、统计和错误 |
| `gpu_nodes` | 节点、BMC、序列号、预期卡数、节点状态 |
| `gpu_assets` | 槽位、UUID、型号、PCI Bus ID、身份状态 |
| `asset_observations` | 每批次发现的原始身份和来源，用于回放与对账 |
| `asset_change_events` | 新增、离线、恢复、替换、下架及字段变化 |
| `collector_targets` | 四类 target 当前状态和错误 |
| `metric_capabilities` | 型号×指标能力矩阵 |
| `data_quality_issues` | 数据质量问题和处理状态 |
| `data_quality_audits` | 人工确认和状态变更审计 |

关键索引：

- `gpu_nodes(cluster_id, host_ip)` unique
- `gpu_assets(node_id, gpu_index)` unique for active slot
- `gpu_assets(gpu_uuid)` unique when not null
- `collector_targets(job, instance)` unique
- `data_quality_issues(status, severity, code)`

### 5.8 后端 API

所有列表 API 支持 `page/page_size/sort/order`，`page_size` 最大 200。

| API | 用途 |
|---|---|
| `GET /api/v1/fleet/summary` | 节点、槽位、UUID、型号和覆盖概览 |
| `GET /api/v1/nodes` | 节点列表与四类 target 状态 |
| `GET /api/v1/nodes/{node_id}` | 节点、BMC、8 卡槽位、问题和同步来源 |
| `GET /api/v1/gpus` | GPU 资产列表，支持 UUID/主机/型号/状态过滤 |
| `GET /api/v1/gpus/{asset_id}` | GPU 身份、标签、能力状态和关联问题 |
| `GET /api/v1/inventory/changes` | 资产新增、离线、恢复、替换和下架事件 |
| `GET /api/v1/inventory/snapshots` | 同步快照与差异统计 |
| `GET /api/v1/targets` | target 列表和错误分类 |
| `GET /api/v1/data-quality/summary` | 覆盖率、未知数、DOWN/MISSING 数量 |
| `GET /api/v1/data-quality/issues` | 数据质量问题列表 |
| `PATCH /api/v1/data-quality/issues/{id}` | 可选：ACK/RESOLVED/IGNORED 和备注；仅在操作员认证完成后开放 |
| `GET /api/v1/capabilities` | 型号×指标能力矩阵 |
| `GET /api/v1/sync-runs` | 同步批次、耗时和结果 |
| `GET /api/v1/exports/gpus.csv` | 当前 GPU 资产清单 |
| `GET /api/v1/exports/nodes.csv` | 当前节点与 target 清单 |
| `GET /api/v1/exports/inventory-changes.csv` | 资产变更历史 |

`/api/v1/sync-runs` 支持 `task_type`、`status` 过滤；`/api/v1/inventory/changes` 支持 `node_ip`、`event_type`、`sync_run_id` 过滤。

当前 Atlas 尚无完整用户认证，Phase 0 不向浏览器提供“强制同步 Prometheus”写接口。手动同步通过服务器 CLI 执行；页面“刷新”只重读 Atlas API。未完成操作员认证时，数据质量问题页保持只读，不能仅凭内网环境开放匿名 PATCH。

### 5.9 后端非功能要求

- 所有 Prometheus 和 BMC 操作只读
- API 不返回凭据、内部 token 或完整敏感配置
- 列表 API P95 小于 500ms
- 单次资产同步在 2 分钟内完成
- 任何同步失败不影响告警 webhook 接收
- 数据库写入使用事务
- 同步器有互斥锁，禁止并发重复运行
- 记录同步成功/失败、查询耗时和问题数量
- 生产 7077 与测试 8077 数据库、进程和发布目录隔离

## 6. 前端功能与交互

### 6.1 集群总览

Phase 0 总览只展示资产与覆盖，不展示虚假健康分：

- 节点总数、UP/REACHABLE/DOWN/UNKNOWN
- 预期槽位、已知 UUID、UUID_UNKNOWN
- 4090/H100/H200 数量
- DCGM/GPU/Node/IPMI 的 UP/DOWN/MISSING
- 未处理数据质量问题数量
- 最近成功同步时间和数据年龄
- 重点异常节点和组件列表

KPI 必须同时显示分子和分母，例如 `UUID 720 / 720`，不能只显示百分比。

### 6.2 GPU 资产页

替换当前静态基线和“仅告警关联 GPU”列表，展示全部物理槽位：

| 列 | 说明 |
|---|---|
| 状态 | ACTIVE/HISTORY_ONLY/UUID_UNKNOWN/CONFLICT |
| GPU UUID | 未知时显示 `UNKNOWN`，不生成伪 UUID |
| 主机 | hostname + host IP |
| 卡号 | GPU index |
| 型号 | 标准型号 + 原始 modelName |
| PCI Bus ID | 规范化值 |
| Node/DCGM/GPU/IPMI | 四类采集状态 |
| 数据时间 | 当前/历史、最后同步批次 |
| 问题数 | 点击打开详情抽屉 |

交互：

- 服务端分页，默认 50 条
- UUID、主机名、IP、PCI Bus ID 全局搜索
- 型号、节点状态、身份状态、target 状态筛选
- 筛选条件写入 URL
- CSV 导出使用同一筛选条件
- 点击行打开 GPU 详情抽屉

### 6.3 节点详情

节点详情抽屉/页面包含：

- host IP、hostname、BMC IP、主机序列号候选
- 节点状态及判定依据
- DCGM/GPU/Node/IPMI target 卡片
- 8 个 GPU 槽位表
- UUID/PCI Bus ID/型号冲突
- 当前数据质量问题
- 来源批次和最后更新时间

`NODE DOWN` 与 `DCGM DOWN` 必须使用不同状态和说明。节点 DOWN 时，组件区显示“已抑制”，但仍可查看原始错误。

节点详情增加“资产历史”：首次发现、最近出现、离线/恢复、主机身份变化、GPU 替换候选和下架确认。

### 6.4 数据质量页

页面结构：

1. 覆盖 KPI
2. Target 矩阵
3. 数据质量问题表
4. 型号能力矩阵
5. 同步历史

当前页面已落地覆盖 KPI、非正常 Target 明细和同步审计。非正常 Target 明细展示节点、组件、状态、确定性原因码、抑制状态、原始错误和同步时间；同步审计展示三类同步批次及关联资产变更。

Target 矩阵按节点为行、DCGM/GPU/Node/IPMI 为列：

- UP：绿色
- DOWN：红色
- MISSING：橙色
- STALE：黄色
- UNKNOWN/UNSUPPORTED：灰色
- NODE DOWN 抑制：斜纹灰色，不重复计入红色故障

问题表支持：问题码、严重度、对象、状态和首次/最后时间过滤。人工 ACK、负责人、解决、忽略和备注只有在操作员认证可用时启用；否则只读展示并导出。

### 6.5 指标能力矩阵

按 4090/H100/H200 分列，指标分组为：

- 基础遥测
- 温度/功耗/时钟
- XID/稳定性
- ECC/SRAM/row remap
- PCIe
- NVLink/NVSwitch
- reset/repair 状态

每个单元格显示能力状态、有效样本比例和最后有效时间。`UNSUPPORTED` 不显示为告警。

### 6.6 全局搜索

扩展现有 `Cmd/Ctrl + K`：

- GPU UUID
- hostname/host IP
- BMC IP
- PCI Bus ID
- target instance
- 数据质量 issue ID/code

结果按“GPU / 节点 / Target / 问题 / 页面”分组。选择 GPU 或节点直接打开详情，不先跳到空列表。

默认搜索 ACTIVE/DISCOVERED/DEGRADED/OFFLINE 资产；开启“包含历史资产”后可查询 REPLACED/RETIRED UUID，避免维修复盘时旧卡无法定位。

### 6.7 资产变更页

新增“资产变更”视图：

- 本次同步新增节点/GPU
- 离线与恢复节点/GPU
- UUID、PCI Bus ID、型号、主机序列号变化
- GPU/节点替换候选
- target 新增、移除和状态变化
- RETIRING/RETIRED 待确认项
- 任意两个同步快照的差异对比

高风险变更（同槽位 UUID 改变、节点序列号改变、批量节点消失）置顶，并允许关联维修记录。Phase 0 未完成操作员认证时只读展示，不提供匿名确认按钮。

### 6.8 数据状态组件

前端新增可复用组件：

```text
NodeStateBadge
TargetStateBadge
IdentityStateBadge
FreshnessBadge
CoverageKpi
TargetMatrix
CapabilityMatrix
DataQualityIssueTable
SyncRunStatus
AssetLifecycleBadge
InventoryChangeTable
SnapshotDiff
GpuAssetTable
NodeDetailDrawer
GpuIdentityDrawer
```

页面必须覆盖：加载、部分失败、无数据、数据过期、同步失败、权限不足和未知状态。未知使用 `UNKNOWN`，不得默认为健康。

### 6.9 响应式要求

- 桌面：完整表格和矩阵
- 平板：侧栏折叠，表格保留横向滚动
- 手机：KPI 和问题卡片；资产表切换为摘要卡片
- 中英文、亮色/暗色/彩色/跟随系统继续生效
- 约 720 条资产量下页面不一次性渲染全部行

## 7. 前后端契约

### 7.1 统一响应元数据

```json
{
  "data": [],
  "meta": {
    "page": 1,
    "page_size": 50,
    "total": 720,
    "snapshot_id": "inv_20260716_165121",
    "collected_at": "2026-07-16T16:51:21+08:00",
    "data_age_seconds": 42,
    "partial": false
  }
}
```

`partial=true` 时必须返回 `warnings[]`，前端显示部分数据，不把整页误判为加载失败。

### 7.2 状态枚举

状态枚举由后端返回机器值和显示所需上下文，前端只负责翻译，不自行推导：

```json
{
  "state": "MISSING",
  "reason_code": "TARGET_NOT_IN_ACTIVE_CONFIG",
  "reason": "target absent from active target list",
  "suppressed": false
}
```

### 7.3 时间语义

所有响应明确区分：

- `collected_at`：从上游采集时间
- `synced_at`：写入 Atlas 时间
- `last_success_at`：最后成功样本时间
- `data_age_seconds`：数据年龄

前端不得只显示模糊的“刚刚更新”。

## 8. 十个工作日实施拆分

| 工作日 | 后端 | 前端 | 产物 |
|---|---|---|---|
| D1 | 从监控生成首批 90 节点/720 槽位启动快照；表结构评审 | 页面信息架构、状态枚举评审 | Schema、API 草案、启动快照 |
| D2 | Prometheus 客户端与查询保护 | API mock、状态组件 | target/current GPU 查询 |
| D3 | 资产同步与字段规范化 | 总览覆盖 KPI | 节点/GPU 入库 |
| D4 | target 分类、节点抑制和生命周期状态 | GPU 资产表 | 资产列表 API |
| D5 | 历史 UUID 恢复、冲突/替换检测 | 节点/GPU 详情抽屉 | 720 槽位可见 |
| D6 | 数据清洗和哨兵值测试 | 数据质量问题表 | issue API |
| D7 | 能力矩阵统计 | Target/能力矩阵 | 4090/H100/H200 矩阵 |
| D8 | 同步批次、变更事件、CSV 导出 | 全局搜索、资产变更、URL 状态 | 导出、搜索和变更历史 |
| D9 | 新增/替换/离线/恢复回放、性能测试 | 响应式、中英文、主题、错误态 | 8077 候选版本 |
| D10 | 现场回放、修正、验收 | 现场验收 | Phase 0 基线版本 |

若只有 1 名全栈开发，建议周期调整为 12～15 个工作日；1 名后端和 1 名前端并行可在 8～10 个工作日完成。

## 9. 验收标准

### 9.1 资产正确性

- 90 个 GPU 节点全部有资产记录
- 720 个物理槽位全部可见，不因节点/target DOWN 消失
- 已知 720 个 UUID 零重复
- `.25` 的 8 个历史 UUID 标记 `HISTORY_ONLY`
- `.37` 的 8 个槽位标记 `UUID_UNKNOWN`，不得虚构 UUID
- 任一 UUID 可反查 host、gpu index、PCI Bus ID、型号和来源批次
- 模拟新增节点后下一次对账可发现，但不会覆盖既有资产
- 模拟同槽位 UUID 变化后生成替换候选，旧 UUID 仍可查询
- 模拟 target 临时消失后资产不删除，只更新状态
- 资产确认下架后进入 RETIRED，历史事件和告警引用仍有效

“映射正确率 100%”指所有已知映射无冲突；UUID 未知必须显式计入覆盖缺口，不得为了达到 100% 填充假数据。

### 9.2 状态正确性

- `.37` 标记 NODE DOWN，四类组件问题被抑制但可查看
- `.25` 标记 REACHABLE，不得误判 NODE DOWN
- `.130` 的 DCGM/GPU DOWN 与节点状态分开
- 其他 GPU exporter DOWN 全部进入待确认列表
- 非 GPU 节点的 Node/IPMI DOWN 不进入 GPU 资产统计

### 9.3 数据清洗

- DCGM 哨兵值、NaN、Inf 零误判为真实数值
- UNSUPPORTED 不生成硬件告警
- 缺失数据输出 UNKNOWN 并降低覆盖率
- 所有 counter 查询使用增量/速率语义

### 9.4 前端

- 总览、GPU 资产、节点详情、数据质量、能力矩阵使用真实 API
- 搜索 UUID/host/IP/BDF 能定位唯一对象
- 720+ 行服务端分页无明显卡顿
- 亮/暗/彩色/系统主题和中英文可用
- 390px、768px、1280px、1440px 无页面级横向溢出
- 所有页面显示快照时间和部分失败状态

### 9.5 稳定性与安全

- 同步失败不覆盖上一成功快照
- 连续运行 24 小时无重复资产和并发同步
- 不影响 7077 生产实例、Prometheus 和 GPU 任务
- 无远程重启、压测、调度或 BMC 写操作
- 日志和 API 不出现明文凭据

## 10. Phase 0 退出条件

满足以下条件后进入 Phase 1：

1. 能从当前监控与资产文档自动建立资产库；启动快照中的 90 节点、720 槽位全部入库并可查询。
2. 720 个已知 UUID 映射无冲突；主机名不含 `gpu` 的节点不进入 GPU 资产清单。
3. 四类 target 状态与节点状态能够正确分离和抑制。
4. 4090/H100/H200 关键指标能力状态明确。
5. 无效值不会进入后续健康评分输入。
6. 前端能够定位任何资产缺口和采集异常。
7. 数据同步、API 和页面在 8077 连续稳定运行并通过现场验收。
8. 新增、替换、离线、恢复和下架回放通过，任何临时监控缺失都不会物理删除资产。

Phase 1 只能消费 Phase 0 输出的规范化资产、能力状态和有效值；不得重新在评分模块中实现一套身份映射或哨兵值判断。

## 11. Atlas 平台完善建议

以下能力按“先形成可靠运维闭环，再增加智能能力”的顺序建设。

### 11.1 第一优先级：平台基础

#### 资产生命周期与拓扑

- 节点、BMC、PCIe 槽位、GPU UUID 和 target 形成关系图
- 所有替换保留前后资产关系
- 支持当前资产和历史资产查询
- 批量节点消失触发“监控配置/网络异常”候选，不直接生成大量硬件故障

#### 人工维修反馈与硬件台账

当前项目不依赖调度系统和任务上下文。硬件负责人通过平台人工录入维修反馈，形成资产生命周期和后续模型标签。

必须支持：

- 选择节点/GPU UUID 或数据质量问题创建维修记录
- 记录现象、诊断、根因分类、处理动作和时间
- 记录送修/RMA、厂商、工单号和备注
- GPU/节点更换时填写旧/新 UUID 或主机序列号
- 记录修复、未发现故障、误报、报废和下架结论
- 记录复测方式、复测结果和是否恢复上线
- 上传或关联日志、照片、检测报告等附件引用
- 所有修改保留操作人、时间和前后值

维修状态建议：

```text
DRAFT -> CONFIRMED -> IN_REPAIR -> REPAIRED/REPLACED
      -> VALIDATED -> CLOSED
```

维修记录只保存和关联人工操作，不由 Atlas 自动执行硬件动作。

#### 权限、凭据和审计

- BMC、SSH、Prometheus 使用集中密钥管理和只读账号
- 前端引入登录、RBAC 和操作审计
- 角色至少分为 viewer/operator/admin
- reset、reboot、drain、benchmark 永远需要明确权限和二次确认
- API、日志、导出文件不得包含明文凭据

#### 平台自身可观测性

Atlas 必须监控自身：

- 同步延迟、最后成功时间和失败次数
- Prometheus 查询耗时、超时和返回序列数
- API 延迟、错误率和并发
- 数据库大小、锁等待、备份状态
- 告警接收积压和通知失败
- 前端版本、后端版本和 schema 版本

平台自身异常不能被解释为 GPU 故障。

### 11.2 第二优先级：故障运维闭环

#### XID 与系统日志事件化

- Atlas Agent 增量读取 journald/kernel
- 解析 XID、NVRM、AER、OOM、driver reset
- PCI Bus ID 映射 GPU UUID
- gauge 状态与真实日志事件分开
- 保存原始日志引用、规则版本和首次/最近时间

#### 告警聚合、抑制和路由

- 同一物理问题产生的 XID、ECC、BMC、AER 和 exporter 告警聚合为一个案例
- 节点 DOWN 抑制其下 exporter/GPU 衍生告警
- 维护窗口抑制预期告警
- 支持重复计数、升级、恢复和重新打开
- 按集群、型号、严重度和责任组路由

#### 故障案例与维修反馈

状态建议：

```text
DETECTED -> TRIAGED -> MAINTENANCE_PENDING -> ISOLATED
         -> DIAGNOSING -> REPAIRED/REPLACED -> VALIDATED -> CLOSED
```

必须记录：确认故障、误报、根因、维修动作、替换前后 UUID、复测结果、责任人和时间。该数据是后续监督故障预测最重要的标签来源。

建议在 Phase 0 完成后立即安排 Phase 0.5，先交付硬件负责人可用的最小反馈功能：

后端数据：

```text
maintenance_records
maintenance_attachments
maintenance_asset_changes
maintenance_audits
```

最小 API：

```text
GET  /api/v1/maintenance-records
POST /api/v1/maintenance-records
GET  /api/v1/maintenance-records/{id}
PATCH /api/v1/maintenance-records/{id}
POST /api/v1/maintenance-records/{id}/validate
POST /api/v1/maintenance-records/{id}/close
```

前端功能：

- 节点/GPU 详情页直接“创建维修记录”
- 待跟进、维修中、待复测、已关闭列表
- 根因和动作使用枚举，备注保留自由文本
- 选择“更换 GPU/节点”后强制填写旧/新身份
- 维修关闭前强制填写复测结论
- 资产变更事件自动关联维修记录
- 支持 CSV 导出，作为后续训练标签和复盘数据

写接口只向 operator/admin 开放。维修表单不包含重启、reset、drain 或 benchmark 的远程执行按钮。

#### SOP 与安全动作门禁

- 每种 XID/ECC/掉卡问题绑定版本化 SOP
- 页面展示前置条件、风险和审批状态
- 首期只提供命令建议和复制，不自动执行
- 后续若增加自动化，必须另行接入安全前置条件、超时、回滚和审计；当前版本不执行

### 11.3 第三优先级：健康与性能能力

#### 可解释健康评分

- 4090/H100/H200 分型号规则
- health score、anomaly score、failure probability 分离
- 每次扣分绑定指标、时间窗、规则版本和数据置信度
- 数据不足返回 UNKNOWN，不输出看似精确的低分

#### 算力衰减检测

- 在线阶段只使用无侵入硬件遥测和同机卡对比
- 没有任务上下文时，不使用业务吞吐、step time 或利用率高低直接判定算力衰减
- 可识别 effective clock、功耗、温度、throttle、PCIe/NVLink 状态等硬件侧异常候选
- 硬件负责人确认维护窗口且 GPU 无任务后，手工执行标准化 benchmark 形成最终结论

#### 主动验证工作台

- 待验证队列
- 安全门禁
- 最小测试计划
- 测试前后软件/功耗配置记录
- 同型号正常基线
- confirmed/false-positive/恢复结论

### 11.4 第四优先级：模型与智能助手

#### PyOD 异常检测

- 按型号、驱动/CUDA 大版本和负载类型分组
- 首批使用 ECOD、Isolation Forest、COPOD 等可回放基线
- shadow mode 运行
- 统计每 1000 GPU·天误报数
- 异常分不得命名为故障概率

#### 监督故障预测

只有维修和故障标签成熟后实施：

- 明确预测目标：掉卡、DBE、GPU 不可用等
- 输出 1h/6h/24h 校准概率
- 按 GPU UUID 和时间隔离训练/测试
- 评估召回率、精确率、提前量、校准度和告警负担

#### LLM + Skill

适合负责：

- 汇总原始信息
- 解释规则与模型结果
- 生成根因候选和排查步骤
- 查询历史相似案例
- 生成日报、周报和复盘

不适合负责：

- 直接生成健康分
- 修改原始指标和故障状态
- 无审批执行重启、reset、drain 或压测

大模型部署优先级低于资产、事件、数据质量和维修标签。

### 11.5 工程架构建议

- 当前规模先保持 Go 单体，按 `inventory/quality/incidents/health/prometheus` 模块拆包，不急于微服务化
- SQLite 继续用于 8077 开发；进入高频事件、多人写入和长期审计前迁移 PostgreSQL
- Prometheus 保存时序；Atlas 只保存资产、快照、事件、特征索引、评分和审计
- 高频统计使用 recording rules 或后台预聚合，避免页面临时查询长时间范围
- 所有规则、清洗、能力矩阵和模型配置版本化
- 建立可回放测试集，发布前重放资产新增、节点 DOWN、GPU 替换、XID、ECC 和数据缺失场景
- 生产发布采用 schema 迁移、数据库备份、版本回滚和 feature flag

### 11.6 建议新增页面

| 页面 | 目标 |
|---|---|
| 资产变更 | 查看新增、替换、离线、恢复和下架 |
| 拓扑 | 节点/BMC/PCIe/GPU/target 关系 |
| 数据质量 | 覆盖、target、能力矩阵和缺失问题 |
| 故障案例 | 分诊、责任人、维修、复测和关闭 |
| 规则与策略 | 规则版本、阈值、启用范围和 shadow 状态 |
| 验证记录 | 人工维护确认、benchmark 和复测结果 |
| 平台状态 | Atlas 同步、API、数据库、通知和版本状态 |
| 审计日志 | 人工确认、配置变更和安全动作记录 |
