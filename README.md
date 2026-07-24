# Atlas

<div align="center">
  <h3>Infrastructure Hardware Reliability Workbench</h3>
  <p>面向 GPU 集群并可扩展至服务器、存储和网络基础设施的硬件可靠性工作台。</p>
</div>

---

## 平台定位

ATLAS 是面向 GPU 集群并可扩展至服务器、存储和网络基础设施的硬件可靠性工作台，提供资产对账、监控数据质量发现、硬件健康评分、故障检测、硬件故障预警与预测、性能衰减识别、故障事件管理以及维修验证闭环。

Atlas 不是单纯的告警转发器，也不是只看大盘的监控页面。

当前已经确定的平台定位是：

- **统一接入层**
  - 接收告警、指标以及后续的日志与健康数据
- **分析中枢**
  - 故障检测识别已经发生或正在发生的硬件故障；故障发生前的风险预警归入硬件故障预警与预测
  - 已确定以 AI + Skill 根据事件与详情生成故障报告、根因分析和处理建议，辅助人工处置；该能力目前仅完成实现路径设计，尚未开发
- **监控数据质量发现**
  - 仅依靠 Prometheus 指标、Target、时序连续性和资产对账，也能识别覆盖缺失、数据失效、指标漂移和资产不一致
  - 不要求必须登录被监控节点，适用于只有监控读取权限的集群
- **知识沉淀层**
  - 将案例、SOP、日志模式、故障经验沉淀成可复用资产
- **可靠性工作台**
  - 以接收记录页、告警详情页、分析报告页等形式支撑排障与迭代

一句话概括：

> Atlas = GPU 健康 + 监控数据质量发现 + 故障事件与维修闭环

Atlas 的数据治理能力定位为“发现、定位、记录和验证”，不是自动修改。平台不会自行登录节点修复 exporter、修改 Prometheus 配置、重启服务或清洗源数据；具备节点/BMC 只读权限时可补充证据，不具备时仍可运行监控数据模式。

资产管理以 [LXOPEasier](https://10.111.201.1:7090/) 的机房、机柜和设备记录为事实来源。Atlas 不替代资产主数据平台：当前维护从 Prometheus/DCGM 得到的监控侧观测资产库，后续通过接口同步 LXOPEasier 事实资产，并持续识别两侧的新增、替换、下架、身份及状态差异。

## 当前已实现

### 接入与处理

- 支持 Atlas 原生告警接口：`POST /api/v1/webhook/alert`
- 支持飞书兼容告警接口：`POST /open-apis/bot/v2/hook/{token}`
- 已兼容飞书文本消息、`post` 消息、交互卡片中的文本提取
- 已兼容一类真实中文飞书告警样式，例如：
  - 网络失活类告警
  - GPU XID 类告警
- 支持告警去重、重复计数、异步处理重试、回调确认、失败记录

### 页面与工作台

- 平台概览支持维护实例名称、产品名称、产品副标题和环境标识，保存后统一更新侧边栏、面包屑、环境徽标和浏览器标题
- GPU 健康页以一行四项展示平均分、风险卡、数据未知和规则命中；“已评分平均分”同时标明已评分/全量口径，UNKNOWN 单独统计且不参与平均
- GPU 节点范围以资产目录中 GPU 主机名为准；退休、CPU 或已移出目录的节点不会因历史 exporter Target 产生 GPU 数据缺失问题
- GPU 身份、四类 Target 与健康评分统一按 10 分钟周期更新；任务完成后才启动下一周期，页面会提示超过 10 分钟仍未完成的采集
- “数据统计”位于“运行”导航末尾，统计数据质量、资产身份和节点可用性问题；硬件故障在“告警中心”查看详情并录入根因、方案、解决过程和结果
- 性能验证页提供 `degradation v0.1.0` 被动影子检测：仅在高负载快照上比较同节点同型号或同型号群体的 SM 时钟中位数，输出候选、基线、性能比、置信度和人工复测建议；不影响健康分，不输出故障概率，不自动隔离或压测
- 接收记录页支持：
  - 按主机、来源、级别筛选
  - 按消息、标签、原始 payload 搜索
  - 按标签 key / value 过滤
  - 查看接收记录列表与解析结果
- 告警详情页支持：
  - 分析草稿
  - 建议动作
  - 证据
  - 解析标签详情
  - 原始 payload 展开查看
- 保留异步失败记录面板，便于快速排障

### AI 分析底座

- 已引入 `AIAnalysisReport` 数据模型
- 当前先用规则化草稿打通 `告警 -> 分析报告` 闭环
- 对 XID / 网络可达性类告警已能生成初始分析草稿

### GPU 资产与采集覆盖（Phase 0）

- 从 Prometheus active targets 与 DCGM 指标持续发现 GPU 节点
- 建立节点、8 卡物理槽位、GPU UUID、四类 exporter target 的持久化资产库
- DCGM target 不可用时，按配置窗口恢复历史 UUID
- 目标消失只更新状态，不删除历史资产
- 同槽位 UUID 变化生成资产变更事件
- 前端总览、GPU 资产、数据质量和全局搜索读取真实资产 API
- 支持无节点登录权限的监控数据模式：识别 Target 异常、指标缺失/陈旧、采集覆盖不足和资产漂移
- 数据质量问题只读输出，不自动修改 exporter、监控配置或节点

## 核心演进方向

后续核心能力聚焦 3 条主线：

### 1. 告警主线

- 接收
- 存储
- 展示
- AI 分析
- 处理建议输出
- 相关日志收集

### 2. 日志主线

- 用户上传系统 / 硬件 / 应用日志
- 做日志归因与 AI 分析
- 基于案例与 skills 生成专业结论

### 3. 健康主线

- GPU 健康评分
- 风险信号识别
- 故障预测
- 第一阶段采用 `规则评分 + AI 解释增强`

核心设计基线：

- [GPU 健康评分与故障预测开发蓝图 v1](docs/gpu-health-and-failure-prediction-blueprint-v1.md)
- [GPU 健康评分 v1 专题草案](docs/gpu-health-score-v1.md)
- [平台能力模块与版本基线](docs/platform-capability-modules.md)
- [事件链路与故障特征库实施方案](docs/event-pipeline-and-fault-feature-catalog-plan.md)：接收记录分页/新鲜度、事件分层、故障特征目录、性能衰减和参考项目适配
- [数据统计与告警处置 v0.3](docs/issue-center-v1.md)：数据问题统计、硬件告警详情、人工根因/解决过程和 AI/Skill 训练数据
- [性能衰减检测 v0.1](docs/performance-degradation-v0.1.md)：被动影子候选、同类基线、API、安全边界与已知限制

### 当前事件数据说明

- “接收记录”来自 Alertmanager/飞书兼容 Webhook，每次接收持久化并保留原始 payload。
- “硬件事件”来自 Atlas 健康规则和后续 XID/ECC/掉卡事件化，同一活动故障会聚合次数和最近发生时间。
- “故障案例”用于后续处置、维修、验证和根因标签闭环。
- `incident v0.3.0` 已实现接收记录与硬件告警的真实总数、稳定 ID 游标分页和服务端筛选；硬件告警关联统一处置台账，可查看详情并追加人工处置记录。
- 硬件事件 API 同样使用不可变 ID 游标；事件严重度、状态或最近观测时间变化时，不会破坏正在进行的分页遍历。
- 测试环境 `8077` 的业务写入仍使用独立测试库；接收记录查询通过 SQLite 只读连接读取生产告警库，因此可查看生产最新告警和完整历史，但不能修改生产记录。
- `/api/v1/data-freshness` 分别输出接收、资产同步和健康评分的源时间、数据年龄、SLA 与 `fresh/stale/empty/error` 状态；页面顶部展示源时间而不是浏览器刷新时间。

### 故障特征与预测路径

Feature Catalog v1.5 已提供 36 个版本化定义，其中 35 个健康特征对应 53 个 DCGM/gpu_exporter/Prometheus 在线源查询；metric-family 变化以单节点 recording-rule canary 保持 shadow，尚未发布到 Prometheus。数据质量页联合检查每卡样本连续性和采集链路，并按分类展示历史、已解决、遗留与当前检测统计；结构异常不扣硬件健康分。Correctable Row Remap 使用 1h/24h 增量判断，稳定累计值不扣分；Uncorrectable ECC 与 Row Remap Failure 只告警和记录，不自动操作任务或节点。详见 [Feature Catalog v1](docs/feature-catalog-v1.md)、[Recording Rule Canary v1](docs/recording-rule-canary-v1.md)、[GPU 双源健康评分契约 v1.4](docs/gpu-dual-source-health-v1.1.md) 和 [性能衰减检测 v0.1](docs/performance-degradation-v0.1.md)。

## 快速开始

### 环境要求

- Go 1.21+
- Node.js 18+
- macOS 本地开发或 Linux amd64 部署环境

### 1. 安装前端依赖

```bash
cd web
npm install
```

### 2. 配置后端

默认配置文件：

```text
configs/config.yaml
```

示例：

```yaml
gateway:
  port: ":8080"
  webhook_token: ""
  feishu_webhook_token: ""
storage:
  dsn: "atlas.db"
feishu:
  bots:
    - enabled: false
      webhook_url: "https://open.feishu.cn/open-apis/bot/v2/hook/YOUR_WEBHOOK_URL"
      enable_signature: false
      secret: ""
logging:
  dir: "logs"
web:
  static_dir: "web/dist"
```

### 3. macOS 本地启动后端

```bash
bash scripts/start_backend_mac.sh
```

使用自定义配置：

```bash
bash scripts/start_backend_mac.sh /path/to/custom-config.yaml
```

使用环境变量覆盖端口：

```bash
ATLAS_PORT=18080 bash scripts/start_backend_mac.sh
```

### 4. macOS 本地启动前端

```bash
bash scripts/start_frontend_mac.sh
```

自定义前端端口：

```bash
ATLAS_WEB_PORT=4174 bash scripts/start_frontend_mac.sh
```

如果后端不是默认 `8080`：

```bash
ATLAS_API_TARGET=http://127.0.0.1:18080 bash scripts/start_frontend_mac.sh
```

### 5. Linux amd64 交叉编译

```bash
bash scripts/build_linux_amd64.sh
```

输出目录默认：

```text
bin/linux-amd64
```

产物：

- `atlas-server`
- `atlas-agent`

说明：

- 项目使用 SQLite CGO 驱动，macOS 交叉编译 Linux 时需要 `zig` 或 `x86_64-linux-gnu-gcc`

### 6. 生产环境部署 Web 界面

Atlas 支持由 `atlas-server` 直接托管前端打包后的静态产物，无需额外反向代理。

先在本地构建前端：

```bash
cd web
npm install
npm run build
```

然后将这些内容上传到服务器：

- `atlas-server`
- `configs/config.yaml`
- `web/dist`

在配置中指定静态目录：

```yaml
web:
  static_dir: "/ops/atlas/web/dist"
```

或使用环境变量覆盖：

```bash
ATLAS_WEB_DIR=/ops/atlas/web/dist ./atlas-server --config /ops/atlas/configs/config.yaml
```

部署完成后：

- Web 页面：`http://<server>:7077/`
- API 状态：`http://<server>:7077/api/v1/status`
- 健康检查：`http://<server>:7077/health`

### 8077 独立测试环境

GPU 平台前端开发使用独立测试实例，不重启或覆盖 `7077` 生产服务：

```bash
bash scripts/deploy_test_8077.sh
```

- 测试页面：`http://10.111.201.1:8077/`
- systemd：`atlas-test.service`
- 工作目录：`/ops/atlas-test`
- 静态发布：`/ops/atlas-test/releases/<timestamp>` + `web-current` 软链接
- 数据库：首次部署从生产 SQLite 生成独立时间点副本，后续不与生产共享写入
- 测试配置：`configs/config.test.yaml`，飞书机器人和 webhook token 默认关闭

## 配置与启动文档

- 配置、端口、自定义配置路径、脚本说明：
  - `docs/configuration.md`
- 告警接入与接口说明：
  - `docs/webhook-alert-api.md`
- 飞书兼容接入规范：
  - `docs/feishu-alert-ingestion-spec-v1.md`
- Alertmanager 模板参考：
  - `docs/alertmanager-feishu-template-v1.md`
- 实施清单：
  - `docs/implementation-checklist-v1.md`
- GPU 健康评分设计：
  - `docs/gpu-health-score-v1.md`
- 故障 / 日志 case 模板：
  - `docs/fault-log-case-template-v1.md`

## API 概览

- `POST /api/v1/webhook/alert`
  - Atlas 原生告警接入
- `POST /open-apis/bot/v2/hook/{token}`
  - 飞书兼容告警接入
- `GET /api/v1/alerts/ingestions`
  - 查询最近接收记录
- `GET /api/v1/alerts/ingestions/{id}/analysis`
  - 查询某条接收记录的分析草稿
- `GET /api/v1/alerts/failures`
  - 查询异步处理失败记录
- `POST /api/v1/push/metrics`
  - 接收 Agent 推送指标
- `GET /api/v1/status`
  - 系统状态检查
- `GET /api/v1/fleet/summary`
  - GPU 节点、槽位、UUID、target 覆盖摘要
- `GET /api/v1/nodes`、`GET /api/v1/nodes/{id-or-ip}`
  - 节点资产列表与节点/8 卡/target 详情
- `GET /api/v1/gpus`
  - GPU 物理槽位与当前 UUID 清单
- `GET /api/v1/targets`
  - DCGM、GPU、Node、IPMI exporter 覆盖矩阵
- `GET /api/v1/sync-runs`
  - 资产同步批次与失败记录
- `GET /api/v1/inventory/changes`
  - GPU UUID 更换等资产变更事件
- `GET /api/v1/fault-events`
  - GPU 健康规则事件 episode，支持状态、严重度、规则、节点和 UUID 过滤
- `GET /api/v1/fault-events/summary`
  - 未恢复事件及严重度汇总

## 技术栈

- **后端**: Go, SQLite, GORM
- **前端**: React, Vite, Tailwind CSS, Framer Motion, i18next
- **当前分析模式**: 规则草稿 + AIAnalysisReport 数据模型
- **目标演进**: 规则、skills、AI 大模型、知识库协同

## 后续重点

- 告警分析草稿替换为真实 AI 分析调用
- 日志上传与日志分析工作台
- GPU 健康评分与预测
- skills / 知识库 / case 回流体系
- SOP 与建议动作体系

## 许可证

MIT License
