# Atlas

<div align="center">
  <h3>AI 驱动的可靠性接入与分析平台</h3>
  <p>从告警接收出发，逐步演进为面向故障分析、日志归因、GPU 健康评分与运维知识沉淀的综合可靠性平台。</p>
</div>

---

## 平台定位

Atlas 不是单纯的告警转发器，也不是只看大盘的监控页面。

当前已经确定的平台定位是：

- **统一接入层**
  - 接收告警、指标以及后续的日志与健康数据
- **分析中枢**
  - 对告警、日志、GPU 健康信号做规则分析与 AI 分析
- **知识沉淀层**
  - 将案例、SOP、日志模式、故障经验沉淀成可复用资产
- **可靠性工作台**
  - 以接收记录页、告警详情页、分析报告页等形式支撑排障与迭代

一句话概括：

> Atlas = 告警接入 + 分析工作台 + AI 可靠性能力底座

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
