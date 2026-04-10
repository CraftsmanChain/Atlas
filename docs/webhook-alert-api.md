# Webhook 告警接入 API

本文档用于告警平台（如 Prometheus Alertmanager、Grafana、Zabbix 或自定义告警系统）对接 Atlas 的告警接收接口。

## 1. 接口信息

- 方法: `POST`
- 路径: `/api/v1/webhook/alert`
- Content-Type: `application/json`
- 成功响应: `202 Accepted`

完整示例 URL:

```text
http://<atlas-host>:8080/api/v1/webhook/alert
```

## 2. 鉴权（可选，推荐启用）

当 `configs/config.yaml` 中配置了 `gateway.webhook_token` 非空时，请求头必须携带:

- `X-Webhook-Token: <your-token>`

否则返回:

- `401 Unauthorized`

如果未配置 `gateway.webhook_token`（为空字符串），则不校验该头。

## 3. 请求体格式

请求体为 JSON，对应字段如下:

- `source` string, 必填，告警来源系统
- `message` string, 必填，告警内容
- `level` string, 选填，告警级别（如 `critical` / `warning`）
- `host` string, 选填，主机名或实例标识
- `timestamp` string(time), 选填，告警时间（RFC3339 格式）
- `labels` object[string]string, 选填，告警标签
- `callback_url` string, 选填，处理完成后 Atlas 回调的 URL
- `callback_token` string, 选填，Atlas 回调时附带请求头 `X-Callback-Token`

请求示例:

```json
{
  "source": "prometheus",
  "level": "critical",
  "message": "CPU usage > 90% for 5m",
  "host": "node-01",
  "timestamp": "2026-04-10T10:30:00Z",
  "labels": {
    "instance": "10.0.0.11:9100",
    "alertname": "HighCPUUsage",
    "team": "sre"
  }
}
```

## 4. 返回码说明

- `202 Accepted`: 已接收成功，后续异步分析与入库
- `400 Bad Request`: JSON 非法或缺少必填字段（`source`/`message`）
- `401 Unauthorized`: 已启用 token 校验但请求头 token 不匹配
- `405 Method Not Allowed`: 非 `POST` 方法
- `500 Internal Server Error`: 服务读取请求体失败

成功响应体示例:

```json
{
  "status": "accepted",
  "message": "Alert event received successfully",
  "request_id": 123
}
```

## 5. 接收后行为

接口成功接收后会执行:

- 打印告警核心字段日志（source、level、message、host、labels、timestamp）
- 打印原始完整 payload 日志
- 异步进入 Analyzer 做去重与增强
- 告警数据写入 SQLite（重复告警会更新重复次数与最后出现时间）
- 异步处理失败自动重试（最多 3 次，指数退避）
- 如配置 `callback_url`，回调确认结果（最多 3 次重试）
- 处理链路状态会记录到 `alert_ingestion_records`，便于页面展示与 AI 分析

日志默认写入 `./logs`，按天自动轮转（可通过 `logging.dir` 修改）。

## 6. curl 调用示例

不启用 token:

```bash
curl -X POST "http://127.0.0.1:8080/api/v1/webhook/alert" \
  -H "Content-Type: application/json" \
  -d '{
    "source":"prometheus",
    "level":"critical",
    "message":"Disk usage > 85%",
    "host":"db-01",
    "timestamp":"2026-04-10T11:00:00Z",
    "labels":{"mount":"/data","team":"infra"}
  }'
```

启用 token:

```bash
curl -X POST "http://127.0.0.1:8080/api/v1/webhook/alert" \
  -H "Content-Type: application/json" \
  -H "X-Webhook-Token: your-webhook-token" \
  -d '{
    "source":"prometheus",
    "level":"warning",
    "message":"Memory usage > 80%",
    "host":"app-01",
    "callback_url":"https://your-alert-platform/callback",
    "callback_token":"your-callback-token"
  }'
```

## 7. 失败查询接口（页面展示）

- 方法: `GET`
- 路径: `/api/v1/alerts/failures`
- 查询参数: `limit`（可选，默认 50，最大 200）

返回示例:

```json
{
  "items": [
    {
      "id": 123,
      "event_id": "a1b2c3d4e5f6g7h8",
      "source": "prometheus",
      "level": "critical",
      "message": "CPU usage > 90% for 5m",
      "process_status": "failed",
      "process_attempts": 3,
      "process_last_error": "database is locked",
      "callback_status": "pending"
    }
  ],
  "total": 1
}
```
