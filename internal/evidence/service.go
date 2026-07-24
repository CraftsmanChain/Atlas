package evidence

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

var ErrEventNotFound = errors.New("fault event not found")

type Service struct {
	db  *storage.DB
	now func() time.Time
}

func NewService(db *storage.DB) *Service {
	return &Service{db: db, now: time.Now}
}

func (s *Service) BuildBundle(eventID uint) (Bundle, error) {
	var event api.GPUFaultEvent
	if err := s.db.First(&event, eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Bundle{}, ErrEventNotFound
		}
		return Bundle{}, err
	}
	bundle := Bundle{
		SchemaVersion: EvidenceSchemaVersion,
		GeneratedAt:   s.now(),
		FaultEvent:    event,
		Entity: Entity{
			Type: "gpu", GPUAssetID: event.GPUAssetID, GPUUUID: event.GPUUUID,
			NodeIP: event.NodeIP, GPUIndex: event.GPUIndex, ModelName: event.ModelName,
		},
		RuleHits:    []api.GPUHealthRuleHit{},
		Resolutions: []api.IssueResolution{},
		Evidence:    []Item{},
		Timeline:    []TimelineEntry{},
		SourceStatus: []SourceStatus{
			{Source: "atlas_database", Status: "available", Detail: text("Atlas 事件与健康数据库可用", "Atlas event and health database is available")},
			{Source: "node_readonly", Status: "not_collected", Detail: text("v0.1 未触发节点命令", "v0.1 did not invoke node commands")},
			{Source: "bmc_readonly", Status: "not_collected", Detail: text("v0.1 未触发 BMC 查询", "v0.1 did not invoke BMC queries")},
		},
		Limitations: defaultLimitations(),
	}
	bundle.Evidence = append(bundle.Evidence, Item{
		ID: fmt.Sprintf("fault-event:%d", event.ID), Kind: "fault_event", Source: event.Source,
		ObservedAt: event.LastObservedAt, Summary: text(
			fmt.Sprintf("%s 在 %s GPU %d 命中", event.RuleCode, event.NodeIP, event.GPUIndex),
			fmt.Sprintf("%s matched on %s GPU %d", event.RuleCode, event.NodeIP, event.GPUIndex),
		),
		Detail: map[string]any{
			"state": event.State, "severity": event.Severity, "domain": event.Domain,
			"evidence": event.Evidence, "observed_value": event.ObservedValue,
			"threshold": event.Threshold, "occurrence_count": event.OccurrenceCount,
			"rule_version": event.RuleVersion,
		},
		Provenance: "gpu_fault_events",
	})

	if event.LatestScoreID == 0 {
		bundle.MissingEvidence = append(bundle.MissingEvidence, gap("health_snapshot", "事件未关联健康评分快照", "The event has no linked health score snapshot"))
	} else {
		var score api.GPUHealthScore
		if err := s.db.First(&score, event.LatestScoreID).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return Bundle{}, err
			}
			bundle.MissingEvidence = append(bundle.MissingEvidence, gap("health_snapshot", "关联的健康评分快照不存在", "The linked health score snapshot is unavailable"))
		} else {
			bundle.HealthScore = &score
			bundle.Evidence = append(bundle.Evidence, scoreEvidence(score))
			if score.FeatureSnapshotID == 0 {
				bundle.MissingEvidence = append(bundle.MissingEvidence, gap("feature_snapshot", "健康评分未关联特征快照", "The health score has no linked feature snapshot"))
			} else {
				var snapshot api.GPUFeatureSnapshot
				if err := s.db.First(&snapshot, score.FeatureSnapshotID).Error; err != nil {
					if !errors.Is(err, gorm.ErrRecordNotFound) {
						return Bundle{}, err
					}
					bundle.MissingEvidence = append(bundle.MissingEvidence, gap("feature_snapshot", "关联的特征快照不存在", "The linked feature snapshot is unavailable"))
				} else {
					bundle.FeatureSnapshot = &snapshot
					bundle.Evidence = append(bundle.Evidence, featureEvidence(snapshot))
				}
			}
			if err := s.db.Where("health_score_id = ?", score.ID).Order("evaluated_at ASC, id ASC").Find(&bundle.RuleHits).Error; err != nil {
				return Bundle{}, err
			}
			for _, hit := range bundle.RuleHits {
				bundle.Evidence = append(bundle.Evidence, ruleHitEvidence(hit))
			}
		}
	}

	var issue api.PlatformIssue
	err := s.db.Where("detection_source = ? AND source_record_id = ?", "health_rule", event.ID).Order("id DESC").First(&issue).Error
	switch {
	case err == nil:
		bundle.Issue = &issue
		bundle.Evidence = append(bundle.Evidence, issueEvidence(issue))
		if err := s.db.Where("issue_id = ?", issue.ID).Order("created_at ASC, id ASC").Find(&bundle.Resolutions).Error; err != nil {
			return Bundle{}, err
		}
		for _, resolution := range bundle.Resolutions {
			bundle.Evidence = append(bundle.Evidence, resolutionEvidence(resolution))
		}
		if len(bundle.Resolutions) == 0 {
			bundle.MissingEvidence = append(bundle.MissingEvidence, gap("operator_resolution", "尚无人工处置结论", "No operator resolution has been recorded"))
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		bundle.MissingEvidence = append(bundle.MissingEvidence, gap("issue_record", "故障事件尚未关联处置台账", "The fault event has no linked resolution record"))
	default:
		return Bundle{}, err
	}
	bundle.MissingEvidence = append(bundle.MissingEvidence,
		gap("node_logs", "尚未采集故障时间窗内的节点日志", "Node logs for the incident window have not been collected"),
		gap("bmc_evidence", "尚未采集 BMC 传感器与 SEL 证据", "BMC sensor and SEL evidence has not been collected"),
	)
	sort.SliceStable(bundle.Evidence, func(i, j int) bool {
		if bundle.Evidence[i].ObservedAt.Equal(bundle.Evidence[j].ObservedAt) {
			return bundle.Evidence[i].ID < bundle.Evidence[j].ID
		}
		return bundle.Evidence[i].ObservedAt.Before(bundle.Evidence[j].ObservedAt)
	})
	for _, item := range bundle.Evidence {
		bundle.Timeline = append(bundle.Timeline, TimelineEntry{At: item.ObservedAt, EvidenceID: item.ID, Label: item.Summary})
	}
	return bundle, nil
}

func (s *Service) BuildReport(eventID uint) (Report, error) {
	bundle, err := s.BuildBundle(eventID)
	if err != nil {
		return Report{}, err
	}
	eventRef := fmt.Sprintf("fault-event:%d", bundle.FaultEvent.ID)
	report := Report{
		SchemaVersion: ReportSchemaVersion, ReportVersion: ReportVersion, GeneratedAt: s.now(),
		AnalysisMode: "deterministic", EventID: bundle.FaultEvent.ID, Severity: bundle.FaultEvent.Severity,
		AffectedEntity: bundle.Entity, Timeline: bundle.Timeline, MissingEvidence: bundle.MissingEvidence,
		NoActionExecuted: true, Limitations: defaultLimitations(),
		Title: text(
			fmt.Sprintf("%s · %s GPU %d", bundle.FaultEvent.RuleCode, bundle.FaultEvent.NodeIP, bundle.FaultEvent.GPUIndex),
			fmt.Sprintf("%s · %s GPU %d", bundle.FaultEvent.RuleCode, bundle.FaultEvent.NodeIP, bundle.FaultEvent.GPUIndex),
		),
		Summary: text(
			fmt.Sprintf("Atlas 检测到 %s 级 %s 事件，当前状态为 %s，累计命中 %d 次。", bundle.FaultEvent.Severity, bundle.FaultEvent.Domain, bundle.FaultEvent.State, bundle.FaultEvent.OccurrenceCount),
			fmt.Sprintf("Atlas detected a %s %s event. Current state is %s with %d occurrence(s).", bundle.FaultEvent.Severity, bundle.FaultEvent.Domain, bundle.FaultEvent.State, bundle.FaultEvent.OccurrenceCount),
		),
		Findings: []Finding{{
			Code: "deterministic_rule_match", Severity: bundle.FaultEvent.Severity,
			Summary:     text(bundle.FaultEvent.Evidence, bundle.FaultEvent.Evidence),
			EvidenceIDs: []string{eventRef},
		}},
	}
	if bundle.HealthScore != nil {
		scoreText := "unknown"
		if bundle.HealthScore.Score != nil {
			scoreText = fmt.Sprintf("%d", *bundle.HealthScore.Score)
		}
		report.Findings = append(report.Findings, Finding{
			Code: "health_context", Severity: bundle.HealthScore.Level,
			Summary: text(
				fmt.Sprintf("关联健康分 %s，数据置信度 %s。", scoreText, bundle.HealthScore.DataConfidence),
				fmt.Sprintf("Linked health score %s with data confidence %s.", scoreText, bundle.HealthScore.DataConfidence),
			),
			EvidenceIDs: []string{fmt.Sprintf("health-score:%d", bundle.HealthScore.ID)},
		})
	}
	report.Hypotheses = append(report.Hypotheses, domainHypothesis(bundle.FaultEvent, eventRef))
	report.RecommendedReadonlyChecks = domainChecks(bundle.FaultEvent.Domain)
	if len(bundle.Resolutions) > 0 {
		latest := bundle.Resolutions[len(bundle.Resolutions)-1]
		if strings.TrimSpace(latest.RootCause) != "" {
			ref := fmt.Sprintf("resolution:%d", latest.ID)
			report.Findings = append(report.Findings, Finding{
				Code: "operator_root_cause", Severity: "info",
				Summary:     text("人工记录的根因："+latest.RootCause, "Operator-recorded root cause: "+latest.RootCause),
				EvidenceIDs: []string{ref},
			})
			report.Hypotheses = append([]Hypothesis{{
				Code: "operator_confirmed", Status: "supported",
				Title:  text("人工处置结论", "Operator resolution"),
				Reason: text(latest.RootCause, latest.RootCause), EvidenceIDs: []string{ref},
			}}, report.Hypotheses...)
		}
	}
	report.OperatorActions = []Text{
		text("由一线工程师核对证据完整度并确认或修正根因。", "Have an engineer review evidence completeness and confirm or correct the root cause."),
		text("任何隔离、重启、维修或主动诊断均需独立人工审批；本报告未执行这些动作。", "Any isolation, restart, repair or active diagnostic requires separate human approval; this report executed none of them."),
	}
	return report, nil
}

func scoreEvidence(score api.GPUHealthScore) Item {
	return Item{
		ID: fmt.Sprintf("health-score:%d", score.ID), Kind: "health_score", Source: "atlas_health",
		ObservedAt: score.EvaluatedAt, Summary: text(
			fmt.Sprintf("GPU 健康等级 %s，数据置信度 %s", score.Level, score.DataConfidence),
			fmt.Sprintf("GPU health level %s with confidence %s", score.Level, score.DataConfidence),
		),
		Detail:     map[string]any{"score": score.Score, "level": score.Level, "data_confidence": score.DataConfidence, "rule_version": score.RuleVersion},
		Provenance: "gpu_health_scores",
	}
}

func featureEvidence(snapshot api.GPUFeatureSnapshot) Item {
	return Item{
		ID: fmt.Sprintf("feature-snapshot:%d", snapshot.ID), Kind: "feature_snapshot", Source: "feature_catalog",
		ObservedAt: snapshot.ObservedAt, Summary: text(
			fmt.Sprintf("特征快照包含 %d/%d 个预期指标", snapshot.AvailableMetricCount, snapshot.ExpectedMetricCount),
			fmt.Sprintf("Feature snapshot contains %d/%d expected metrics", snapshot.AvailableMetricCount, snapshot.ExpectedMetricCount),
		),
		Detail: map[string]any{
			"metrics": snapshot.Metrics, "metric_sources": snapshot.MetricSources,
			"sources_available": snapshot.SourcesAvailable, "feature_catalog_version": snapshot.FeatureCatalogVersion,
			"data_confidence": snapshot.DataConfidence,
		},
		Provenance: "gpu_feature_snapshots",
	}
}

func ruleHitEvidence(hit api.GPUHealthRuleHit) Item {
	return Item{
		ID: fmt.Sprintf("rule-hit:%d", hit.ID), Kind: "rule_hit", Source: "atlas_health",
		ObservedAt: hit.EvaluatedAt, Summary: text(hit.Evidence, hit.Evidence),
		Detail: map[string]any{
			"rule_code": hit.RuleCode, "domain": hit.Domain, "severity": hit.Severity,
			"observed_value": hit.ObservedValue, "threshold": hit.Threshold,
			"deduction": hit.Deduction, "rule_version": hit.RuleVersion,
		},
		Provenance: "gpu_health_rule_hits",
	}
}

func issueEvidence(issue api.PlatformIssue) Item {
	return Item{
		ID: fmt.Sprintf("issue:%d", issue.ID), Kind: "issue", Source: "atlas_issue_ledger",
		ObservedAt: issue.LastDetectedAt, Summary: text(issue.Title, issue.Title),
		Detail:     map[string]any{"status": issue.Status, "detection_state": issue.DetectionState, "description": issue.Description},
		Provenance: "platform_issues",
	}
}

func resolutionEvidence(resolution api.IssueResolution) Item {
	return Item{
		ID: fmt.Sprintf("resolution:%d", resolution.ID), Kind: "resolution", Source: "operator",
		ObservedAt: resolution.CreatedAt, Summary: text(
			fmt.Sprintf("人工处置状态更新为 %s", resolution.Status),
			fmt.Sprintf("Operator updated workflow status to %s", resolution.Status),
		),
		Detail: map[string]any{
			"status": resolution.Status, "root_cause": resolution.RootCause, "solution": resolution.Solution,
			"resolution_process": resolution.ResolutionProcess, "result": resolution.Result,
			"evidence": resolution.Evidence, "training_eligible": resolution.TrainingEligible,
		},
		Provenance: "issue_resolutions",
	}
}

func domainHypothesis(event api.GPUFaultEvent, evidenceID string) Hypothesis {
	code, zh, en := "hardware_or_software_unknown", "硬件、驱动或环境异常", "Hardware, driver, or environmental anomaly"
	switch strings.ToLower(event.Domain) {
	case "memory":
		code, zh, en = "gpu_memory_subsystem", "GPU 显存或内存子系统异常", "GPU memory subsystem anomaly"
	case "pcie", "interconnect":
		code, zh, en = "pcie_or_interconnect", "GPU、PCIe 插槽、链路或主板侧异常", "GPU, PCIe slot, link, or board-side anomaly"
	case "thermal":
		code, zh, en = "thermal_path", "GPU 散热、风扇或环境温度异常", "GPU cooling, fan, or ambient temperature anomaly"
	case "power":
		code, zh, en = "power_path", "GPU、PSU 或供电路径异常", "GPU, PSU, or power-path anomaly"
	case "stability", "availability":
		code, zh, en = "gpu_driver_or_availability", "GPU、驱动、PCIe 或节点可用性异常", "GPU, driver, PCIe, or node availability anomaly"
	case "performance":
		code, zh, en = "performance_degradation_candidate", "负载不可比、限频或硬件性能衰减候选", "Workload mismatch, throttling, or hardware degradation candidate"
	}
	return Hypothesis{
		Code: code, Status: "possible", Title: text(zh, en),
		Reason:      text("确定性规则提供了异常证据，但缺少节点日志、BMC 和人工诊断，尚不能确认为最终根因。", "A deterministic rule provides anomaly evidence, but node logs, BMC evidence, and operator diagnosis are missing; this is not a confirmed root cause."),
		EvidenceIDs: []string{evidenceID},
	}
}

func domainChecks(domain string) []Text {
	common := []Text{text("读取故障时间窗内的 XID、kernel 和驱动日志。", "Read XID, kernel, and driver logs for the incident window.")}
	switch strings.ToLower(domain) {
	case "memory":
		return append(common, text("核对 ECC、Row Remap 增量、型号支持能力和历史复发。", "Review ECC and row-remap deltas, model support, and recurrence history."))
	case "pcie", "interconnect":
		return append(common, text("核对 PCIe replay、协商宽度、AER 和同机其他 GPU。", "Review PCIe replay, negotiated width, AER, and peer GPUs on the node."))
	case "thermal":
		return append(common, text("核对 GPU/显存温度、时钟、功耗以及 BMC 风扇和进风温度。", "Review GPU/memory temperature, clocks, power, BMC fans, and inlet temperature."))
	case "power":
		return append(common, text("核对 GPU 功耗、功率上限以及 BMC PSU、电压和 SEL。", "Review GPU power, power limits, and BMC PSU, voltage, and SEL evidence."))
	case "performance":
		return append(common, text("确认负载可比性，并核对时钟、功耗、温度、PCIe/NVLink 和同类基线。", "Confirm workload comparability and review clocks, power, temperature, PCIe/NVLink, and peer baselines."))
	default:
		return append(common, text("核对 UUID/指标消失、reset-required、节点可达性和同机 GPU 状态。", "Review UUID/metric disappearance, reset-required, node reachability, and peer GPU state."))
	}
}

func defaultLimitations() []Text {
	return []Text{
		text("这是确定性分析辅助，不是已确认根因。", "This is deterministic analysis assistance, not a confirmed root cause."),
		text("v0.1 仅聚合 Atlas 已有只读数据，未执行节点、BMC、任务或配置操作。", "v0.1 only aggregates existing read-only Atlas data and performs no node, BMC, workload, or configuration actions."),
	}
}

func text(zh, en string) Text     { return Text{ZH: zh, EN: en} }
func gap(code, zh, en string) Gap { return Gap{Code: code, Detail: text(zh, en)} }
