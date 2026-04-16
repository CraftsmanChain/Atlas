# Alertmanager 到 Atlas 飞书兼容接入模板 v1

本文档给出第一阶段推荐的 Alertmanager 消息模板写法，目标是让 Alertmanager 继续按“飞书机器人消息”发送，但内容采用 Atlas 可解析的结构化文本。

## 1. 使用目标

- 保持告警发送链路仍然是飞书机器人消息形态
- 让 Atlas 通过飞书兼容入口接收告警
- 将 Alertmanager 的核心字段和自定义标签一并带入 Atlas

## 2. 推荐字段

第一阶段推荐固定以下顶层字段：

- `source`
- `level`
- `host`
- `message`
- `labels`
- `timestamp`

其余自定义字段建议先放在：

- `labels`

如果后续某些字段使用频率高，再升级为顶层字段。

## 3. 推荐消息文本模板

推荐生成如下文本：

```text
[atlas-alert]
source=alertmanager
level={{ .CommonLabels.severity }}
host={{ .CommonLabels.instance }}
message={{ .CommonAnnotations.summary }}
labels=alertname={{ .CommonLabels.alertname }},instance={{ .CommonLabels.instance }},job={{ .CommonLabels.job }},cluster={{ .CommonLabels.cluster }},namespace={{ .CommonLabels.namespace }},pod={{ .CommonLabels.pod }},gpu={{ .CommonLabels.gpu }}
timestamp={{ (index .Alerts 0).StartsAt }}
```

## 4. 模板说明

- `source`
  - 固定写 `alertmanager`
- `level`
  - 推荐取 `severity`
- `host`
  - 推荐取 `instance`
- `message`
  - 推荐优先取 `summary`
- `labels`
  - 放 Alertmanager 标签和你的自定义标签
- `timestamp`
  - 推荐使用首条告警的开始时间

## 5. 关于自定义标签

如果你有一些自定义标签，例如：

- `cluster`
- `gpu`
- `gpu_uuid`
- `team`
- `service`
- `env`

第一阶段建议全部直接拼到 `labels=` 这一行中，例如：

```text
labels=alertname={{ .CommonLabels.alertname }},instance={{ .CommonLabels.instance }},cluster={{ .CommonLabels.cluster }},gpu={{ .CommonLabels.gpu }},gpu_uuid={{ .CommonLabels.gpu_uuid }},team={{ .CommonLabels.team }}
```

Atlas 侧会：

- 解析 `labels=` 行内的键值对
- 对结构化文本中的其他未知字段，也自动落到 `labels`

## 6. 自定义字段扩展方式

如果你希望保留一些额外字段，也可以这样写：

```text
[atlas-alert]
source=alertmanager
level={{ .CommonLabels.severity }}
host={{ .CommonLabels.instance }}
alertname={{ .CommonLabels.alertname }}
cluster={{ .CommonLabels.cluster }}
gpu={{ .CommonLabels.gpu }}
message={{ .CommonAnnotations.summary }}
timestamp={{ (index .Alerts 0).StartsAt }}
```

对于 `alertname`、`cluster`、`gpu` 这类当前未定义为顶层字段的内容，Atlas 第一阶段会自动归入 `labels`。

## 7. 推荐实践

- 尽量保证 `message` 简洁明确
- 自定义标签尽量保持小写并使用稳定 key
- 不要在同一个 key 中塞过长文本
- 如果某些标签可能为空，先允许为空，后续再优化模板

## 8. 下一步建议

后续如果你提供一份真实的 Alertmanager 模板上下文或真实告警样例，可以继续做两件事：

- 按你的实际字段补一版可直接复制的模板
- 进一步增强 Atlas 的字段映射规则

## 9. 当前已兼容的真实中文样式

Atlas 当前除了支持 `[atlas-alert]` 结构化文本，也兼容你现有的这类中文飞书告警文本：

```text
网络失活-外网
级别状态:  次要 [啊？]Triggered
告警名称: 网络失活-外网
告警标签:
   - app: blackbox-exporter
   - instance: 10.111.101.4
触发时间: 2026-04-14 17:00:03
发送时间: 2026-04-14 17:00:46
触发时值:  1
```

以及：

```text
XID故障-低优先级
级别状态:  次要 [啊？]Triggered
告警名称: XID故障-低优先级
告警标签:
   - Hostname: 4090GPU-03
   - err_code: 43
   - err_msg: GPU stopped processing
触发时间: 2026-03-21 14:18:51
发送时间: 2026-03-21 14:19:05
触发时值:  43
```

当前映射规则：

- `source`
  - 默认映射为 `alertmanager`
- `level`
  - 从 `级别状态` 解析，当前 `次要` 会映射为 `warning`
- `message`
  - 优先取 `告警名称`
- `host`
  - 优先取标签中的 `Hostname`，其次 `instance`，再次 `ext`
- `labels`
  - `告警标签` 下的所有 `key: value`
  - `发送时间` 映射为 `sent_at`
  - `触发时值` 映射为 `trigger_value`
  - 原始中文等级会保留为 `severity_text`

这意味着你当前的飞书消息即使暂时不改成 `[atlas-alert]`，也已经可以先接入 Atlas。
