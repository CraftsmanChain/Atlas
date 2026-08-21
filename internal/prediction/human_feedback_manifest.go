package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"atlas/pkg/api"
)

const HumanFeedbackManifestVersion = "prediction-human-feedback-manifest-v1"

type HumanFeedbackRecord struct {
	Source              string     `json:"source"`
	ReferenceID         uint       `json:"reference_id"`
	EntityKey           string     `json:"entity_key"`
	GPUUUID             string     `json:"gpu_uuid,omitempty"`
	NodeIP              string     `json:"node_ip,omitempty"`
	ModelName           string     `json:"model_name,omitempty"`
	EventType           string     `json:"event_type,omitempty"`
	Outcome             string     `json:"outcome,omitempty"`
	QualityTier         string     `json:"quality_tier,omitempty"`
	Operator            string     `json:"operator,omitempty"`
	OccurredAt          time.Time  `json:"occurred_at,omitempty"`
	AvailableAt         time.Time  `json:"available_at,omitempty"`
	PredictionWindowEnd *time.Time `json:"prediction_window_end_at,omitempty"`
	Status              string     `json:"status"`
	BlockingReasons     []string   `json:"blocking_reasons"`
}

type HumanFeedbackManifest struct {
	Version                          string                `json:"version"`
	FrameworkVersion                 string                `json:"framework_version"`
	LabelContract                    string                `json:"label_contract_version"`
	OutcomeRuleVersion               string                `json:"outcome_rule_version"`
	Mode                             string                `json:"mode"`
	Status                           string                `json:"status"`
	ManifestSHA256                   string                `json:"manifest_sha256"`
	TotalLabels                      int                   `json:"total_labels"`
	HumanConfirmedLabels             int                   `json:"human_confirmed_labels"`
	ConfirmedPositiveLabels          int                   `json:"confirmed_positive_labels"`
	HumanOverrideOutcomes            int                   `json:"human_override_outcomes"`
	HumanPositiveOutcomes            int                   `json:"human_positive_outcomes"`
	MatchedPredictionWindows         int                   `json:"matched_prediction_windows"`
	MatchedPositivePredictionWindows int                   `json:"matched_positive_prediction_windows"`
	ConfirmedLabelsWithoutMatch      int                   `json:"confirmed_labels_without_prediction_window_match"`
	LabelsMissingIdentity            int                   `json:"labels_missing_identity"`
	LabelsMissingAvailability        int                   `json:"labels_missing_available_at"`
	OutcomeOverridesMissingOperator  int                   `json:"outcome_overrides_missing_operator"`
	PointInTimeViolations            int                   `json:"point_in_time_violations"`
	ByEventType                      map[string]int        `json:"by_event_type"`
	ByModel                          map[string]int        `json:"by_model"`
	ByOutcome                        map[string]int        `json:"by_outcome"`
	FeedbackPolicy                   []string              `json:"feedback_policy"`
	BlockingReasons                  []string              `json:"blocking_reasons"`
	RecommendedNextRun               []string              `json:"recommended_next_run"`
	SampleRecords                    []HumanFeedbackRecord `json:"sample_records"`
	GeneratedAt                      time.Time             `json:"generated_at"`
}

func (s *Service) HumanFeedbackManifest() (HumanFeedbackManifest, error) {
	var labels []api.FailureLabel
	if err := s.db.Where("quality_tier = ? OR confirmation_resolution_id > ? OR confirmed_at IS NOT NULL", "confirmed", 0).
		Order("available_at ASC, id ASC").Find(&labels).Error; err != nil {
		return HumanFeedbackManifest{}, err
	}
	var outcomes []api.PredictionOutcomeEvaluation
	if err := s.db.Where("human_actual_value IS NOT NULL").Order("human_decided_at ASC, id ASC").Find(&outcomes).Error; err != nil {
		return HumanFeedbackManifest{}, err
	}
	manifest := HumanFeedbackManifest{
		Version: HumanFeedbackManifestVersion, FrameworkVersion: FrameworkVersion, LabelContract: LabelContractVersion,
		OutcomeRuleVersion: OutcomeRuleVersion, Mode: "read_only_human_feedback_governance",
		ByEventType: map[string]int{}, ByModel: map[string]int{}, ByOutcome: map[string]int{}, GeneratedAt: s.now(),
		FeedbackPolicy: []string{
			"operator feedback is evidence for validation and future training only; it never triggers automatic remediation",
			"confirmed hardware labels must keep stable identity, occurred_at, available_at, and operator/repair provenance",
			"prediction-window matching must respect the prediction cutoff; feedback available at or before the cutoff is treated as a metadata violation",
			"human outcome overrides are tracked beside rule outcomes and do not erase rule-derived provenance",
		},
	}
	for _, label := range labels {
		manifest.TotalLabels++
		if label.ConfirmationResolutionID > 0 || label.ConfirmedAt != nil || label.QualityTier == "confirmed" {
			manifest.HumanConfirmedLabels++
		}
		if label.LabelValue == 1 && !label.Excluded {
			manifest.ConfirmedPositiveLabels++
			increment(manifest.ByEventType, label.EventType)
			increment(manifest.ByModel, label.ModelName)
		}
		reasons := feedbackLabelBlockers(label)
		manifest.LabelsMissingIdentity += countReason(reasons, "stable identity is missing")
		manifest.LabelsMissingAvailability += countReason(reasons, "available_at is missing")
		manifest.PointInTimeViolations += countReason(reasons, "available_at is before occurred_at")
		matches, positiveMatches, timeViolations := feedbackWindowMatches(label, outcomes)
		manifest.MatchedPredictionWindows += matches
		manifest.MatchedPositivePredictionWindows += positiveMatches
		manifest.PointInTimeViolations += timeViolations
		if label.LabelValue == 1 && !label.Excluded && matches == 0 {
			manifest.ConfirmedLabelsWithoutMatch++
		}
		if len(manifest.SampleRecords) < 20 {
			status := "eligible_feedback"
			if len(reasons) > 0 {
				status = "blocked_metadata"
			}
			manifest.SampleRecords = append(manifest.SampleRecords, HumanFeedbackRecord{
				Source: "failure_label", ReferenceID: label.ID, EntityKey: label.EntityKey, GPUUUID: label.GPUUUID,
				NodeIP: label.NodeIP, ModelName: label.ModelName, EventType: label.EventType, QualityTier: label.QualityTier,
				OccurredAt: label.OccurredAt, AvailableAt: label.AvailableAt, Status: status, BlockingReasons: reasons,
			})
		}
	}
	for _, outcome := range outcomes {
		manifest.HumanOverrideOutcomes++
		increment(manifest.ByOutcome, outcome.HumanOutcome)
		if outcome.HumanActualValue != nil && *outcome.HumanActualValue == 1 {
			manifest.HumanPositiveOutcomes++
		}
		reasons := feedbackOutcomeBlockers(outcome)
		manifest.OutcomeOverridesMissingOperator += countReason(reasons, "operator is missing")
		if len(manifest.SampleRecords) < 20 {
			manifest.SampleRecords = append(manifest.SampleRecords, HumanFeedbackRecord{
				Source: "prediction_outcome", ReferenceID: outcome.ID, EntityKey: outcome.EntityKey, GPUUUID: outcome.GPUUUID,
				NodeIP: outcome.NodeIP, ModelName: outcome.ModelName, EventType: outcome.ScopeEventType, Outcome: outcome.HumanOutcome,
				Operator: outcome.HumanDecidedBy, OccurredAt: outcome.PredictionEvaluatedAt, AvailableAt: humanDecisionTime(outcome),
				PredictionWindowEnd: &outcome.WindowEndAt, Status: feedbackOutcomeStatus(reasons), BlockingReasons: reasons,
			})
		}
	}
	manifest.BlockingReasons, manifest.RecommendedNextRun = humanFeedbackBlockers(manifest)
	manifest.Status = "feedback_ready"
	if len(manifest.BlockingReasons) > 0 {
		manifest.Status = "blocked"
	} else if manifest.MatchedPredictionWindows == 0 {
		manifest.Status = "exploratory_ready"
	}
	manifest.ManifestSHA256 = humanFeedbackManifestChecksum(manifest)
	return manifest, nil
}

func feedbackLabelBlockers(label api.FailureLabel) []string {
	reasons := []string{}
	if strings.TrimSpace(label.GPUUUID) == "" && strings.TrimSpace(label.EntityKey) == "" {
		reasons = append(reasons, "stable identity is missing")
	}
	if label.AvailableAt.IsZero() {
		reasons = append(reasons, "available_at is missing")
	}
	if !label.AvailableAt.IsZero() && !label.OccurredAt.IsZero() && label.AvailableAt.Before(label.OccurredAt) {
		reasons = append(reasons, "available_at is before occurred_at")
	}
	return uniqueSorted(reasons)
}

func feedbackOutcomeBlockers(outcome api.PredictionOutcomeEvaluation) []string {
	reasons := []string{}
	if strings.TrimSpace(outcome.HumanDecidedBy) == "" {
		reasons = append(reasons, "operator is missing")
	}
	if strings.TrimSpace(outcome.HumanReason) == "" {
		reasons = append(reasons, "human reason is missing")
	}
	if outcome.HumanDecidedAt == nil {
		reasons = append(reasons, "human_decided_at is missing")
	}
	return uniqueSorted(reasons)
}

func feedbackOutcomeStatus(reasons []string) string {
	if len(reasons) > 0 {
		return "blocked_metadata"
	}
	return "eligible_feedback"
}

func humanDecisionTime(outcome api.PredictionOutcomeEvaluation) time.Time {
	if outcome.HumanDecidedAt == nil {
		return time.Time{}
	}
	return *outcome.HumanDecidedAt
}

func feedbackWindowMatches(label api.FailureLabel, outcomes []api.PredictionOutcomeEvaluation) (int, int, int) {
	if label.Excluded || label.LabelValue != 1 || label.OccurredAt.IsZero() {
		return 0, 0, 0
	}
	matches, positives, violations := 0, 0, 0
	for _, outcome := range outcomes {
		if !sameFeedbackEntity(label, outcome) {
			continue
		}
		if label.OccurredAt.Before(outcome.WindowStartAt) || label.OccurredAt.After(outcome.WindowEndAt) {
			continue
		}
		matches++
		if outcome.HumanActualValue != nil && *outcome.HumanActualValue == 1 {
			positives++
		}
		if !label.AvailableAt.IsZero() && !outcome.PredictionEvaluatedAt.IsZero() && !label.AvailableAt.After(outcome.PredictionEvaluatedAt) {
			violations++
		}
	}
	return matches, positives, violations
}

func sameFeedbackEntity(label api.FailureLabel, outcome api.PredictionOutcomeEvaluation) bool {
	if label.GPUUUID != "" && outcome.GPUUUID != "" {
		return label.GPUUUID == outcome.GPUUUID
	}
	if label.EntityKey != "" && outcome.EntityKey != "" {
		return label.EntityKey == outcome.EntityKey
	}
	if label.NodeIP != "" && outcome.NodeIP != "" {
		return label.NodeIP == outcome.NodeIP
	}
	return false
}

func countReason(reasons []string, reason string) int {
	for _, value := range reasons {
		if value == reason {
			return 1
		}
	}
	return 0
}

func humanFeedbackBlockers(manifest HumanFeedbackManifest) ([]string, []string) {
	reasons := []string{}
	next := []string{}
	if manifest.HumanConfirmedLabels == 0 && manifest.HumanOverrideOutcomes == 0 {
		reasons = append(reasons, "no human hardware feedback has been recorded")
		next = append(next, "record confirmed hardware failures or reviewed prediction outcomes as they occur")
	}
	if manifest.LabelsMissingIdentity > 0 {
		reasons = append(reasons, "human feedback has labels without stable identity")
		next = append(next, "attach GPU UUID, entity key, or node identity before using feedback for validation")
	}
	if manifest.LabelsMissingAvailability > 0 {
		reasons = append(reasons, "human feedback has labels without available_at")
		next = append(next, "record when the operator evidence became available")
	}
	if manifest.PointInTimeViolations > 0 {
		reasons = append(reasons, "human feedback has point-in-time metadata violations")
		next = append(next, "repair occurred_at and available_at before binding feedback to prediction windows")
	}
	if manifest.OutcomeOverridesMissingOperator > 0 {
		reasons = append(reasons, "human outcome overrides are missing operator identity")
		next = append(next, "require operator identity for every human outcome decision")
	}
	if manifest.HumanConfirmedLabels > 0 && manifest.MatchedPredictionWindows == 0 {
		next = append(next, "continue shadow scoring until confirmed feedback overlaps matured prediction windows")
	}
	if len(next) == 0 {
		next = append(next, "archive this feedback manifest SHA256 with the next validation-readiness report")
	}
	return uniqueSorted(reasons), uniqueSorted(next)
}

func humanFeedbackManifestChecksum(manifest HumanFeedbackManifest) string {
	fingerprint := manifest
	fingerprint.ManifestSHA256 = ""
	fingerprint.GeneratedAt = time.Time{}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
