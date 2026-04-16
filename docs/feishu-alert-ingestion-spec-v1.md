# 飞书兼容告警接入 Spec v1

本文档定义 Atlas 第一阶段的飞书兼容告警接入方案。目标是让只支持“发送飞书机器人消息”的外部告警平台，能以最小改造成本把告警投递到 Atlas。

## 1. 目标

- 兼容飞书机器人 Webhook 的请求路径与基础消息格式
- 将飞书消息解析并归一化成 Atlas 内部告警事件
- 保持后续处理链路与原生告警接入接口一致

## 2. 接口路径

- 方法：`POST`
- 路径：`/open-apis/bot/v2/hook/{token}`

其中 `{token}` 为 Atlas 配置的飞书兼容接收 token。

## 3. 配置项

在 `configs/config.yaml` 中新增：

```yaml
gateway:
  feishu_webhook_token: "your-ingestion-token"
```

说明：

- 当该配置非空时，Atlas 会校验路径中的 token
- 当该配置为空时，Atlas 接受任意非空 token，适合本地调试，不建议生产环境使用

## 4. 支持的消息类型

第一阶段支持：

- `text`
- `post`
- `interactive`

其中最推荐的是 `text`，因为最容易约定结构化文本内容。

## 5. 推荐消息格式

推荐外部告警平台发送如下文本：

```text
[atlas-alert]
source=prometheus
level=critical
host=gpu-node-01
labels=gpu=3,cluster=train-a
message=GPU temperature is too high
timestamp=2026-04-14T12:00:00Z
```

支持的结构化字段：

- `source`
- `level`
- `host`
- `message`
- `labels`
- `timestamp`
- `callback_url`
- `callback_token`

`labels` 采用逗号分隔的 `key=value` 形式，例如：

```text
labels=gpu=3,cluster=train-a,team=sre
```

对于结构化文本中的未知字段，第一阶段 Atlas 会自动将其归入 `labels`，便于兼容 Alertmanager 的自定义标签。

## 6. 解析策略

### 6.1 结构化文本

当消息第一行是 `[atlas-alert]` 时：

- Atlas 按行解析键值对
- 归一化为内部 `AlertEvent`

### 6.2 纯文本兜底

如果消息不是结构化文本：

- `source` 默认记为 `feishu`
- `level` 默认记为 `info`
- `message` 使用原始文本内容

### 6.3 JSON 文本兼容

如果 `text` 内容本身是一个 JSON 对象，Atlas 会尝试将其直接解析为内部告警模型。

## 7. 返回

与原生接口保持一致：

- `202 Accepted`

响应体示例：

```json
{
  "status": "accepted",
  "message": "Alert event received successfully",
  "request_id": 123
}
```

## 8. 第一阶段边界

第一阶段不追求：

- 完整模拟飞书平台返回结构
- 完整覆盖飞书卡片所有嵌套元素
- 钉钉兼容接入

## 9. 后续演进

后续会在该接口基础上继续扩展：

- 钉钉接入适配器
- 解析状态记录
- 更丰富的字段映射
- 告警接收回放与样本测试

## 10. Alertmanager 模板参考

如果告警由 Alertmanager 发送，推荐参考：

- `docs/alertmanager-feishu-template-v1.md`
