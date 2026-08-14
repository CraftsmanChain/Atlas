package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
)

const EvidenceBundleVersion = "prediction-evidence-bundle-v1"

type EvidenceBundleItem struct {
	LabelKey                      string     `json:"label_key"`
	EntityKey                     string     `json:"entity_key"`
	GPUAssetID                    uint       `json:"gpu_asset_id,omitempty"`
	GPUUUID                       string     `json:"gpu_uuid,omitempty"`
	NodeIP                        string     `json:"node_ip,omitempty"`
	ModelName                     string     `json:"model_name,omitempty"`
	LabelValue                    int        `json:"label_value"`
	QualityTier                   string     `json:"quality_tier"`
	EventType                     string     `json:"event_type"`
	SourceType                    string     `json:"source_type"`
	SourceRecordID                uint       `json:"source_record_id"`
	SourceReference               string     `json:"source_reference"`
	RuleVersion                   string     `json:"rule_version,omitempty"`
	ConfirmationResolutionID      uint       `json:"confirmation_resolution_id,omitempty"`
	HumanConfirmationReference    string     `json:"human_confirmation_reference,omitempty"`
	HumanConfirmationTrainingUse  string     `json:"human_confirmation_training_use,omitempty"`
	ExclusionReason               string     `json:"exclusion_reason,omitempty"`
	IncludedInPositiveDenominator bool       `json:"included_in_positive_denominator"`
	EvidenceStatus                string     `json:"evidence_status"`
	OccurredAt                    time.Time  `json:"occurred_at"`
	AvailableAt                   time.Time  `json:"available_at"`
	ConfirmedAt                   *time.Time `json:"confirmed_at,omitempty"`
}

type EvidenceBundleReport struct {
	Version                     string               `json:"version"`
	LabelContract               string               `json:"label_contract_version"`
	Mode                        string               `json:"mode"`
	TotalLabels                 int                  `json:"total_labels"`
	PositiveLabels              int                  `json:"positive_labels"`
	ExcludedLabels              int                  `json:"excluded_labels"`
	ConfirmedLabels             int                  `json:"confirmed_labels"`
	StrongProxyLabels           int                  `json:"strong_proxy_labels"`
	WeakProxyLabels             int                  `json:"weak_proxy_labels"`
	HumanConfirmationReferences int                  `json:"human_confirmation_references"`
	SampleEvidenceLimit         int                  `json:"sample_evidence_limit"`
	BundleSHA256                string               `json:"bundle_sha256"`
	ByEventType                 map[string]int       `json:"by_event_type"`
	BySourceType                map[string]int       `json:"by_source_type"`
	ByRuleVersion               map[string]int       `json:"by_rule_version"`
	ByQualityTier               map[string]int       `json:"by_quality_tier"`
	ExclusionReasons            map[string]int       `json:"exclusion_reasons"`
	PositiveEvidence            []EvidenceBundleItem `json:"positive_evidence"`
	ExcludedEvidence            []EvidenceBundleItem `json:"excluded_evidence"`
	AnswerableQuestions         []string             `json:"answerable_questions"`
	KnownGaps                   []string             `json:"known_gaps"`
	GeneratedAt                 time.Time            `json:"generated_at"`
}

func (s *Service) EvidenceBundleReport() (EvidenceBundleReport, error) {
	var labels []api.FailureLabel
	if err := s.db.Order("available_at ASC, id ASC").Find(&labels).Error; err != nil {
		return EvidenceBundleReport{}, err
	}
	resolutions, err := s.evidenceBundleResolutions(labels)
	if err != nil {
		return EvidenceBundleReport{}, err
	}
	report := EvidenceBundleReport{
		Version: EvidenceBundleVersion, LabelContract: LabelContractVersion, Mode: "read_only_training_sample_evidence_bundle",
		SampleEvidenceLimit: 25, GeneratedAt: s.now(),
		ByEventType: map[string]int{}, BySourceType: map[string]int{}, ByRuleVersion: map[string]int{},
		ByQualityTier: map[string]int{}, ExclusionReasons: map[string]int{},
		AnswerableQuestions: []string{
			"why this positive sample is a positive sample",
			"why this sample was excluded from positive denominators",
			"which rule, source record, quality tier, and human confirmation reference support the label",
		},
	}
	for _, label := range labels {
		item := evidenceBundleItem(label, resolutions[label.ConfirmationResolutionID])
		report.TotalLabels++
		increment(report.ByEventType, label.EventType)
		increment(report.BySourceType, label.SourceType)
		increment(report.ByRuleVersion, label.RuleVersion)
		increment(report.ByQualityTier, label.QualityTier)
		if label.ConfirmationResolutionID > 0 || label.ConfirmedAt != nil {
			report.HumanConfirmationReferences++
		}
		switch label.QualityTier {
		case "confirmed":
			report.ConfirmedLabels++
		case "strong_proxy":
			report.StrongProxyLabels++
		case "weak_proxy":
			report.WeakProxyLabels++
		}
		if label.Excluded {
			report.ExcludedLabels++
			reason := strings.TrimSpace(label.ExclusionReason)
			if reason == "" {
				reason = "unspecified"
			}
			report.ExclusionReasons[reason]++
			if len(report.ExcludedEvidence) < report.SampleEvidenceLimit {
				report.ExcludedEvidence = append(report.ExcludedEvidence, item)
			}
			continue
		}
		if label.LabelValue == 1 {
			report.PositiveLabels++
			if len(report.PositiveEvidence) < report.SampleEvidenceLimit {
				report.PositiveEvidence = append(report.PositiveEvidence, item)
			}
		}
	}
	report.KnownGaps = evidenceBundleKnownGaps(report)
	report.BundleSHA256 = evidenceBundleChecksum(report)
	return report, nil
}

func (s *Service) evidenceBundleResolutions(labels []api.FailureLabel) (map[uint]api.IssueResolution, error) {
	ids := make([]uint, 0, len(labels))
	seen := map[uint]bool{}
	for _, label := range labels {
		if label.ConfirmationResolutionID == 0 || seen[label.ConfirmationResolutionID] {
			continue
		}
		seen[label.ConfirmationResolutionID] = true
		ids = append(ids, label.ConfirmationResolutionID)
	}
	if len(ids) == 0 {
		return map[uint]api.IssueResolution{}, nil
	}
	var rows []api.IssueResolution
	if err := s.db.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]api.IssueResolution, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	return byID, nil
}

func evidenceBundleItem(label api.FailureLabel, resolution api.IssueResolution) EvidenceBundleItem {
	item := EvidenceBundleItem{
		LabelKey: label.LabelKey, EntityKey: label.EntityKey, GPUAssetID: label.GPUAssetID,
		GPUUUID: label.GPUUUID, NodeIP: label.NodeIP, ModelName: label.ModelName,
		LabelValue: label.LabelValue, QualityTier: label.QualityTier, EventType: label.EventType,
		SourceType: label.SourceType, SourceRecordID: label.SourceRecordID,
		SourceReference: sourceReference(label.SourceType, label.SourceRecordID),
		RuleVersion:     label.RuleVersion, ConfirmationResolutionID: label.ConfirmationResolutionID,
		ExclusionReason: label.ExclusionReason, OccurredAt: label.OccurredAt,
		AvailableAt: label.AvailableAt, ConfirmedAt: label.ConfirmedAt,
		IncludedInPositiveDenominator: label.LabelValue == 1 && !label.Excluded,
		EvidenceStatus:                evidenceStatus(label),
	}
	if label.ConfirmationResolutionID > 0 {
		item.HumanConfirmationReference = fmt.Sprintf("issue_resolution:%d", label.ConfirmationResolutionID)
		if resolution.ID == label.ConfirmationResolutionID {
			operator := strings.TrimSpace(resolution.Operator)
			if operator == "" {
				operator = "unknown_operator"
			}
			item.HumanConfirmationReference = fmt.Sprintf("issue_resolution:%d issue:%d operator:%s status:%s", resolution.ID, resolution.IssueID, operator, resolution.Status)
			if resolution.TrainingEligible {
				item.HumanConfirmationTrainingUse = "training_eligible"
			} else {
				item.HumanConfirmationTrainingUse = "not_training_eligible"
			}
		}
	}
	return item
}

func sourceReference(sourceType string, sourceRecordID uint) string {
	sourceType = strings.TrimSpace(sourceType)
	if sourceType == "" {
		sourceType = "unknown_source"
	}
	if sourceRecordID == 0 {
		return sourceType
	}
	return fmt.Sprintf("%s:%d", sourceType, sourceRecordID)
}

func evidenceStatus(label api.FailureLabel) string {
	if label.Excluded {
		return "excluded"
	}
	if label.LabelValue != 1 {
		return "non_positive"
	}
	if label.QualityTier == "confirmed" {
		return "confirmed_positive"
	}
	return "proxy_positive"
}

func evidenceBundleKnownGaps(report EvidenceBundleReport) []string {
	gaps := []string{}
	if report.TotalLabels == 0 {
		gaps = append(gaps, "no materialized failure labels are available for evidence bundling")
	}
	if report.ConfirmedLabels == 0 {
		gaps = append(gaps, "no confirmed human or repair evidence is present")
	}
	if report.HumanConfirmationReferences < report.ConfirmedLabels {
		gaps = append(gaps, "some confirmed labels do not carry a resolution reference")
	}
	if report.ExcludedLabels > 0 {
		gaps = append(gaps, "excluded samples require review before rebuilding training cohorts")
	}
	gaps = append(gaps, "this report summarizes materialized label evidence only; raw feature windows remain in training matrix artifacts")
	sort.Strings(gaps)
	return gaps
}

func evidenceBundleChecksum(report EvidenceBundleReport) string {
	fingerprint := struct {
		Version                     string               `json:"version"`
		LabelContract               string               `json:"label_contract_version"`
		Mode                        string               `json:"mode"`
		TotalLabels                 int                  `json:"total_labels"`
		PositiveLabels              int                  `json:"positive_labels"`
		ExcludedLabels              int                  `json:"excluded_labels"`
		ConfirmedLabels             int                  `json:"confirmed_labels"`
		StrongProxyLabels           int                  `json:"strong_proxy_labels"`
		WeakProxyLabels             int                  `json:"weak_proxy_labels"`
		HumanConfirmationReferences int                  `json:"human_confirmation_references"`
		SampleEvidenceLimit         int                  `json:"sample_evidence_limit"`
		ByEventType                 map[string]int       `json:"by_event_type"`
		BySourceType                map[string]int       `json:"by_source_type"`
		ByRuleVersion               map[string]int       `json:"by_rule_version"`
		ByQualityTier               map[string]int       `json:"by_quality_tier"`
		ExclusionReasons            map[string]int       `json:"exclusion_reasons"`
		PositiveEvidence            []EvidenceBundleItem `json:"positive_evidence"`
		ExcludedEvidence            []EvidenceBundleItem `json:"excluded_evidence"`
		AnswerableQuestions         []string             `json:"answerable_questions"`
		KnownGaps                   []string             `json:"known_gaps"`
	}{
		Version: report.Version, LabelContract: report.LabelContract, Mode: report.Mode,
		TotalLabels: report.TotalLabels, PositiveLabels: report.PositiveLabels, ExcludedLabels: report.ExcludedLabels,
		ConfirmedLabels: report.ConfirmedLabels, StrongProxyLabels: report.StrongProxyLabels, WeakProxyLabels: report.WeakProxyLabels,
		HumanConfirmationReferences: report.HumanConfirmationReferences, SampleEvidenceLimit: report.SampleEvidenceLimit,
		ByEventType: report.ByEventType, BySourceType: report.BySourceType, ByRuleVersion: report.ByRuleVersion,
		ByQualityTier: report.ByQualityTier, ExclusionReasons: report.ExclusionReasons,
		PositiveEvidence: report.PositiveEvidence, ExcludedEvidence: report.ExcludedEvidence,
		AnswerableQuestions: report.AnswerableQuestions, KnownGaps: report.KnownGaps,
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
