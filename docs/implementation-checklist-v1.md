# Atlas 准备工作实施清单 v1

本文档用于梳理 Atlas 后续 AI 能力建设的前置准备项，按 `P0 / P1 / P2` 与 `文档 / skills / schema / 代码模块` 四个维度组织，便于逐项确认和分阶段落地。

## 核心目标

Atlas 后续核心能力聚焦 3 条主线：

1. 告警主线：接收、存储、展示、AI 分析、处理建议、相关日志收集
2. 日志主线：用户上传系统/硬件/应用日志，做 AI 分析与输出专业结论
3. 健康主线：GPU 优先的硬件健康评分、风险识别与故障预测

当前阶段的硬约束：

- 自动处理先不做执行闭环，只输出建议、SOP 和人工确认前的操作预案
- AI 输出必须结构化，不能只返回自然语言段落
- 所有分析结论必须可回溯到原始证据或规则命中项

## 第一优先事项

第一优先事项是建设“告警接收接口”，并优先兼容飞书机器人 Webhook 形态。

原因：

- 现有外部告警平台只支持发送到飞书和钉钉机器人
- Atlas 需要先伪装成飞书侧接收端，降低改造成本
- 告警统一接入后，后续存储、展示、AI 分析、日志联动才有稳定入口

### P0-1 飞书风格告警接收接口目标

建议以“兼容飞书机器人 Webhook 请求格式”为第一阶段目标，而不是要求外部告警平台改造专用 Atlas 格式。

建议目标：

- 提供一个飞书风格接收端点，例如 `/open-apis/bot/v2/hook/:token`
- 接收飞书机器人常见 payload，至少兼容 `text`、`post` 两种消息形态
- 将飞书消息内容解析并归一化为 Atlas 内部 `AlertEvent`
- 支持通过约定格式从消息文本中提取 `source`、`level`、`host`、`message`、`labels`
- 接收成功后写入统一告警流水，不直接与“通知机器人”逻辑耦合

第一阶段不追求：

- 完整兼容飞书所有消息卡片细节
- 完整兼容钉钉所有自定义机器人格式
- 复杂鉴权编排

### P0-2 飞书兼容接收接口建议方案

建议采用“接入适配层 + 内部统一事件模型”：

1. 外部接口层
   - 飞书兼容入口：`/open-apis/bot/v2/hook/:token`
   - 保留 Atlas 原生入口：`/api/v1/webhook/alert`
2. 解析适配层
   - `FeishuWebhookAdapter`
   - 后续预留 `DingTalkWebhookAdapter`
3. 内部标准模型
   - 统一归一化为 `AlertEvent`
   - 原始 payload 存档到 `AlertIngestionRecord.RawPayload`
4. 后续处理链路
   - 去重
   - 增强
   - AI 分析
   - 日志联动
   - 展示

建议消息约定：

- 第一阶段允许外部告警平台发送“结构化文本”
- Atlas 从文本中解析约定字段
- 例如：

```text
[atlas-alert]
source=prometheus
level=critical
host=gpu-node-01
labels=gpu=3,cluster=train-a
message=GPU temperature is too high
```

这样做的好处：

- 兼容飞书机器人现有发送通道
- 外部平台改动最小
- Atlas 内部仍能得到结构化告警数据

## P0

### 文档

- `告警接入 spec v1`
  - 定义飞书兼容接收接口
  - 定义 Atlas 原生接入接口
  - 定义字段映射与解析失败策略
- `AI 能力建设总纲`
  - 明确告警、日志、健康三条主线
  - 明确 AI 在平台中的职责边界
- `GPU 健康评分 spec v1`
  - 定义评分输入、输出、解释字段
- `故障/日志 case 模板 v1`
  - 统一案例沉淀格式

### skills

- `alert-spec-writer`
  - 把告警需求转换成机器可执行 spec
- `planner-dag`
  - 把功能拆成可依赖、可并行、可回滚任务
- `alert-rca-analyst`
  - 面向告警做根因候选分析和处理建议
- `log-forensics-analyst`
  - 面向日志做模式提取、错误归类和结论输出

### schema

- `AlertEvent v2`
  - 增加接入来源类型、原始消息类型、解析结果字段
- `AlertIngestionRecord v2`
  - 增加 adapter 类型、解析状态、解析错误、标准化结果摘要
- `AIAnalysisReport`
  - 用于存储 AI 分析结果、证据、建议、置信度
- `LogArtifact`
  - 用于存储用户上传日志、解析元信息、脱敏状态
- `GPUHealthScore`
  - 用于存储每卡/每机评分结果、命中规则、AI 解释

### 代码模块

- `internal/gateway/adapters`
  - 新增飞书与后续钉钉适配器
- `internal/ingestion`
  - 接收、标准化、校验、落库
- `internal/analysis`
  - AI 分析调度与报告生成
- `internal/knowledge`
  - 案例库、SOP、故障模式检索
- `internal/health`
  - GPU 规则评分与风险信号识别

## P1

### 文档

- `日志分析 spec v1`
  - 定义支持的日志来源、大小、格式、脱敏要求
- `AI 输出 contract v1`
  - 定义告警分析、日志分析、健康评分三类结构化返回
- `知识库设计文档`
  - 定义案例、模式、SOP、参考资料的组织方式
- `验证体系文档`
  - 定义 schema 校验、样本回放、结果验收标准

### skills

- `health-score-analyst`
  - 对 GPU 健康评分结果做解释与风险总结
- `knowledge-curator`
  - 把案例沉淀为知识条目与 SOP
- `review-verifier`
  - 校验 AI 输出是否符合 spec 与证据要求
- `prompt-template-maintainer`
  - 维护 prompt、few-shot、输出 schema 版本

### schema

- `KnowledgeItem`
  - 通用知识条目模型
- `CaseSummary`
  - 故障案例结构化摘要
- `PromptTemplateVersion`
  - prompt、模型、schema 的版本化记录
- `PredictionSignal`
  - 风险趋势、异常点、评分变化轨迹

### 代码模块

- `internal/logs`
  - 日志上传、清洗、切片、脱敏
- `internal/verifier`
  - AI 输出 schema 校验与规则校验
- `internal/workflow`
  - 分析任务 DAG、重试、状态记录
- `pkg/llm`
  - 统一大模型接入抽象

## P2

### 文档

- `故障预测设计文档`
  - 从规则评分演进到统计/监督学习预测
- `成本控制文档`
  - 模型分层、token 控制、early stop
- `失败恢复与 checkpoint 文档`
  - 让 AI 工作流具备回滚与重试能力

### skills

- `test-case-generator`
  - 基于 spec 生成回归样本与边界用例
- `predictor-trainer`
  - 训练与评估 GPU 风险预测模型
- `incident-sop-composer`
  - 自动生成标准处理预案

### schema

- `PredictionDatasetMeta`
  - 训练集元信息
- `ModelEvaluationReport`
  - 模型效果、样本覆盖、误报漏报指标
- `SOPTemplate`
  - 结构化 SOP 模型

### 代码模块

- `internal/predictor`
  - GPU 风险预测模型训练与推理
- `internal/sop`
  - 建议动作、操作预案与人工确认流
- `internal/cost`
  - 模型选择、token 配额与成本监控

## 里程碑建议

### 里程碑 1：告警入口打通

- 飞书风格告警接收接口可用
- 告警可归一化存储
- 页面可查看接收记录
- 可触发基础 AI 分析占位流程

### 里程碑 2：日志分析闭环

- 用户可上传日志
- 日志可脱敏、切片、分析
- AI 可输出结构化分析报告
- 案例可沉淀进知识库

### 里程碑 3：GPU 健康评分闭环

- 可按 GPU/主机输出健康分
- 可展示命中规则与 AI 解释
- 可识别高风险对象并形成趋势记录

### 里程碑 4：预测与知识增强

- 引入训练样本与预测模型
- 形成案例回流、规则回流、prompt 回流机制

## 待确认项

- 是否确认第一阶段优先兼容飞书机器人 Webhook 形态
- 是否接受“结构化文本解析”为第一阶段最小接入方案
- 是否将钉钉兼容放到飞书兼容之后
- 是否将自动处理明确推迟到 AI 建议稳定之后
