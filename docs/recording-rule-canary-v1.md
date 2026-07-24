# Metric-family Recording Rule Canary v1

> 状态：配置与查询验证完成，尚未发布到 Prometheus  
> 范围：`10.114.4.101` 单节点、8 张 H100  
> Feature Catalog：`v1.5.0`

## 目标

`gpu_metric_family_count_delta_5m` 用于识别 GPU 指标族突然消失或恢复。直接在全量 720 GPU 上反复扫描全部 `DCGM_FI_DEV_*` 指标会增加 Prometheus 查询压力，因此必须先通过单节点 recording-rule canary 验证。

Canary 配置位于 `deploy/prometheus/atlas-feature-canary.rules.yml`，只生成：

- `atlas_canary:gpu_metric_family_count`
- `atlas_canary:gpu_metric_family_count_delta_5m`

Catalog 中该特征保持 `shadow`、`experimental`，不进入健康评分，也不生成数据质量问题。

## 已完成验证

- 等价即时 PromQL 在现场返回 8 条 GPU 序列。
- 当前 8 张 GPU 的五分钟指标族数量变化均为 0。
- 查询范围通过 `host_ip="10.114.4.101"` 固定为单节点。
- 自动测试限制规则名前缀、单节点过滤、规则数量和评估周期，避免误提交全量高基数配置。

## 发布门槛

Prometheus 管理员确认并发布 canary 后，至少观察 24 小时：

1. 规则评估无失败。
2. 规则查询耗时 P95 小于 1 秒。
3. 输出序列稳定为 8，正常期 delta 为 0。
4. 对单个指标族短时缺失的测试可产生非零 delta。
5. 未显著增加 Prometheus CPU、内存和 rule evaluation backlog。

只有通过以上门槛，才设计分批扩容；当前配置不得直接改成全量节点。
