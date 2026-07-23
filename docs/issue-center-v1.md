# Atlas 数据统计与告警处置 v0.3

“数据统计”用于回答数据质量、资产身份和节点可用性累计发现多少问题、已经解决多少、仍遗留多少。“告警中心”承载硬件告警及其详情与人工处置。两者共用可审计的问题台账和追加式处置历史，不会因为页面拆分而丢失训练记录。

## 问题来源与分类

当前每分钟归一化五类自动检测源，但页面按职责拆分：

| 检测源 | 问题分类 | 示例 |
| --- | --- | --- |
| GPU 节点状态 | `availability` | degraded、offline、unknown 节点 |
| GPU 资产状态 | `inventory` | UUID unknown、history only、identity conflict |
| Collector Target | `data_quality` | down、missing、scrape failed |
| 当前健康评分 | `data_quality` | score unknown、数据置信度不足 |
| GPU 硬件规则事件 | `hardware_fault` | XID、Row Remap、温度、PCIe 等；在“告警中心”展示和处置 |

“数据统计”的汇总和默认列表不包含 `hardware_fault`，避免硬件告警在两个页面重复计数。底层 `PlatformIssue` 和 `IssueResolution` 仍完整保留；告警中心通过事件的 `issue_id` 打开同一详情与处置抽屉，人工记录仍可进入训练数据。

`detection_state` 表示自动检测源当前是 `active` 还是 `cleared`；`status` 表示人工工作流的 `open`、`in_progress`、`resolved` 或 `ignored`。两个状态必须分开，避免“人工记录已处理”和“监控源已经恢复”互相覆盖。

## API

- `GET /api/v1/issues/summary`：数据问题的累计发现、已解决、遗留、忽略、当前仍被检测，以及分类/状态/级别统计；不包含硬件故障。
- `GET /api/v1/issues`：默认返回数据问题；支持 `category`、`status`、`severity`、`detection_state`、`issue_type`、`q` 和稳定 ID 游标分页。显式指定 `category=hardware_fault` 时仍可读取硬件处置台账。
- `GET /api/v1/fault-events`：硬件告警列表，并稳定输出关联的 `issue_id`、`workflow_status` 和 `latest_resolution_id`。
- `GET /api/v1/issues/{id}`：问题详情和全部人工处置历史。
- `POST /api/v1/issues/{id}/resolution`：追加状态、根因、方案、解决过程、结果、证据、处理人与训练资格。
- `GET /api/v1/issues/training-data`：输出 `atlas-issue-training-v1` 数据集。

## AI 与 Skill 数据质量门

人工处置记录采用追加式保存，不覆盖历史尝试。只有同时满足以下条件才允许标记 `training_eligible=true`：

1. 问题状态为 resolved。
2. 根本原因、解决方案、解决过程和处理结果完整。
3. 操作人员确认内容已脱敏。

训练数据可用于根因分类、相似案例召回、处理步骤建议和问题处理 Skill，但不能未经审核直接用于自动执行重启、隔离、维修或压测操作。

## 后续迭代

- 增加负责人、优先级、SLA、评论、附件和审批。
- 将同一实体的重复问题组织为 episode/case，保留复发历史。
- 接入维修工单、复测结果和重新上线验证。
- 建立数据集快照、版本、脱敏审核和训练/评估划分。
