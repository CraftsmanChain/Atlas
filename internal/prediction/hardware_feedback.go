package prediction

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

const HardwareFaultFeedbackRequestVersion = "hardware-fault-feedback-request-v1"

type HardwareFaultFeedbackInput struct {
	NodeIP           string `json:"node_ip"`
	GPUUUID          string `json:"gpu_uuid"`
	ReportedGPUUUID  string `json:"reported_gpu_uuid"`
	GPUIndex         int    `json:"gpu_index"`
	GPUAssetID       uint   `json:"gpu_asset_id"`
	FaultType        string `json:"fault_type"`
	FaultOccurredAt  string `json:"fault_occurred_at"`
	PreWindowHours   int    `json:"pre_window_hours"`
	PostWindowHours  int    `json:"post_window_hours"`
	Operator         string `json:"operator"`
	Description      string `json:"description"`
	RepairAction     string `json:"repair_action"`
	HardwareReplaced bool   `json:"hardware_replaced"`
	EvidenceNote     string `json:"evidence_note"`
	TrainingEligible bool   `json:"training_eligible"`
}

func (s *Service) HardwareFaultFeedbackRequests(limit int) ([]api.HardwareFaultFeedbackRequest, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []api.HardwareFaultFeedbackRequest
	if err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *Service) CreateHardwareFaultFeedback(input HardwareFaultFeedbackInput) (api.HardwareFaultFeedbackRequest, error) {
	nodeIP := strings.TrimSpace(input.NodeIP)
	gpuUUID := strings.TrimSpace(input.GPUUUID)
	reportedGPUUUID := firstNonEmpty(input.ReportedGPUUUID, gpuUUID)
	faultType := strings.TrimSpace(input.FaultType)
	repairAction := strings.TrimSpace(input.RepairAction)
	operator := strings.TrimSpace(input.Operator)
	if nodeIP == "" {
		return api.HardwareFaultFeedbackRequest{}, fmt.Errorf("node_ip is required")
	}
	if gpuUUID == "" && input.GPUIndex < 0 {
		return api.HardwareFaultFeedbackRequest{}, fmt.Errorf("gpu_uuid or gpu_index is required")
	}
	if faultType == "" {
		return api.HardwareFaultFeedbackRequest{}, fmt.Errorf("fault_type is required")
	}
	if operator == "" {
		return api.HardwareFaultFeedbackRequest{}, fmt.Errorf("operator is required")
	}
	occurredAt, err := parseFeedbackTime(input.FaultOccurredAt)
	if err != nil {
		return api.HardwareFaultFeedbackRequest{}, err
	}
	preWindow := input.PreWindowHours
	if preWindow <= 0 {
		preWindow = 24
	}
	postWindow := input.PostWindowHours
	if postWindow <= 0 {
		postWindow = 24
	}
	if preWindow > 24*30 || postWindow > 24*30 {
		return api.HardwareFaultFeedbackRequest{}, fmt.Errorf("pre/post window cannot exceed 30 days")
	}
	now := s.now()
	replacementFeedback := input.HardwareReplaced || strings.Contains(repairAction, "replace")
	identityStatus := "current_identity_selected"
	identityNote := "selected GPU identity can be used for the fault-time history pack"
	blockingReasons := api.StringList{"offline pre/post monitoring history pack has not been collected yet"}
	if replacementFeedback {
		identityStatus = "requires_historical_identity_at_fault_time"
		identityNote = "hardware replacement was reported; current slot UUID is only a reference and must not be used as the failed GPU identity until historical identity intervals are resolved at fault time"
		blockingReasons = append(blockingReasons, "hardware replacement reported; resolve historical GPU identity at fault time before training")
		gpuUUID = ""
	}
	row := api.HardwareFaultFeedbackRequest{
		RequestKey:               fmt.Sprintf("fault-feedback-%d", now.UnixNano()),
		Status:                   "history_pack_requested",
		NodeIP:                   nodeIP,
		GPUUUID:                  gpuUUID,
		ReportedGPUUUID:          reportedGPUUUID,
		GPUIndex:                 input.GPUIndex,
		GPUAssetID:               input.GPUAssetID,
		FaultType:                faultType,
		FaultOccurredAt:          occurredAt,
		PreWindowHours:           preWindow,
		PostWindowHours:          postWindow,
		Operator:                 operator,
		Description:              strings.TrimSpace(input.Description),
		RepairAction:             repairAction,
		HardwareReplaced:         replacementFeedback,
		EvidenceNote:             strings.TrimSpace(input.EvidenceNote),
		TrainingEligible:         input.TrainingEligible,
		HistoryPackStatus:        "queued_offline_collection",
		IdentityResolutionStatus: identityStatus,
		IdentityResolutionNote:   identityNote,
		WarningReviewStatus:      "pending_warning_review",
		WarningReviewWindowHours: preWindow,
		BlockingReasons:          blockingReasons,
	}
	var asset api.GPUAsset
	query := s.db.Where("node_ip = ?", nodeIP)
	if input.GPUAssetID > 0 {
		query = s.db.Where("id = ?", input.GPUAssetID)
	} else if gpuUUID != "" {
		query = query.Where("current_uuid = ?", gpuUUID)
	} else {
		query = query.Where("gpu_index = ?", input.GPUIndex)
	}
	if err := query.First(&asset).Error; err == nil {
		row.GPUAssetID = asset.ID
		row.ReportedGPUUUID = firstNonEmpty(row.ReportedGPUUUID, asset.CurrentUUID)
		if !replacementFeedback {
			row.GPUUUID = firstNonEmpty(row.GPUUUID, asset.CurrentUUID)
		}
		row.GPUIndex = asset.GPUIndex
		row.ModelName = firstNonEmpty(asset.ModelName, asset.Model)
	}
	row.HistoryPackScope = feedbackHistoryScope(row.NodeIP, row.GPUUUID, row.ReportedGPUUUID, row.GPUIndex, row.FaultOccurredAt, row.PreWindowHours, row.PostWindowHours, row.IdentityResolutionStatus)
	if err := s.db.Create(&row).Error; err != nil {
		return row, err
	}
	return row, nil
}

func (s *Service) ReviewHardwareFaultFeedbackWarning(id uint) (api.HardwareFaultFeedbackRequest, error) {
	if id == 0 {
		return api.HardwareFaultFeedbackRequest{}, fmt.Errorf("feedback request id is required")
	}
	var row api.HardwareFaultFeedbackRequest
	if err := s.db.First(&row, id).Error; err != nil {
		return row, err
	}
	if strings.TrimSpace(row.GPUUUID) == "" || row.IdentityResolutionStatus == "requires_historical_identity_at_fault_time" {
		prepared, err := s.PrepareHardwareFaultFeedbackPack(id)
		if err != nil {
			return row, err
		}
		row = prepared
	}
	windowHours := warningReviewWindowHours(row)
	if strings.TrimSpace(row.GPUUUID) == "" || strings.HasPrefix(row.IdentityResolutionStatus, "blocked") {
		row.WarningReviewStatus = "blocked_identity_required"
		row.WarningReviewWindowHours = windowHours
		row.MatchedWarningCount = 0
		row.MatchedWarningKeys = api.StringList{}
		row.WarningReviewNote = "cannot review shadow warning coverage until the failed GPU identity at fault time is resolved"
		if err := s.db.Save(&row).Error; err != nil {
			return row, err
		}
		return row, nil
	}
	start := row.FaultOccurredAt.Add(-time.Duration(windowHours) * time.Hour)
	end := row.FaultOccurredAt
	var predictions []api.HardwareRiskPrediction
	if err := s.db.Where("node_ip = ? AND gpu_uuid = ? AND evaluated_at >= ? AND evaluated_at <= ?", row.NodeIP, row.GPUUUID, start, end).
		Order("evaluated_at DESC, id DESC").
		Limit(20).
		Find(&predictions).Error; err != nil {
		return row, err
	}
	keys := make(api.StringList, 0, len(predictions))
	positive := 0
	probabilityScored := 0
	thresholds := s.warningReviewThresholds(predictions)
	for _, prediction := range predictions {
		threshold, hasThreshold := thresholds[prediction.ModelSpecID]
		if prediction.Probability != nil {
			probabilityScored++
			if hasThreshold && *prediction.Probability >= threshold {
				positive++
			}
		}
		keys = append(keys, fmt.Sprintf("prediction:%d model=%s status=%s p=%s threshold=%s evaluated=%s expires=%s", prediction.ID, prediction.ModelVersion, prediction.Status, formatProbability(prediction.Probability), formatThreshold(threshold, hasThreshold), prediction.EvaluatedAt.UTC().Format(time.RFC3339), prediction.ExpiresAt.UTC().Format(time.RFC3339)))
	}
	row.WarningReviewWindowHours = windowHours
	row.MatchedWarningCount = len(predictions)
	row.MatchedWarningKeys = keys
	switch {
	case len(predictions) == 0:
		row.WarningReviewStatus = "manual_feedback_no_prior_shadow_warning"
		row.WarningReviewNote = fmt.Sprintf("manual hardware-fault feedback found no read-only shadow warning candidate for node=%s gpu_uuid=%s within %dh before fault; keep as false-negative/coverage review evidence only", row.NodeIP, row.GPUUUID, windowHours)
	case positive > 0:
		row.WarningReviewStatus = "manual_feedback_prior_shadow_candidate_found"
		row.WarningReviewNote = fmt.Sprintf("manual hardware-fault feedback matched %d read-only shadow record(s), including %d above-threshold candidate(s); review outcome alignment before training", len(predictions), positive)
	default:
		row.WarningReviewStatus = "manual_feedback_prior_shadow_below_threshold"
		row.WarningReviewNote = fmt.Sprintf("manual hardware-fault feedback matched %d shadow record(s), %d with persisted probability, but none was above the model decision threshold; review feature coverage and threshold behavior", len(predictions), probabilityScored)
	}
	if err := s.db.Save(&row).Error; err != nil {
		return row, err
	}
	return row, nil
}

func (s *Service) PrepareHardwareFaultFeedbackPack(id uint) (api.HardwareFaultFeedbackRequest, error) {
	if id == 0 {
		return api.HardwareFaultFeedbackRequest{}, fmt.Errorf("feedback request id is required")
	}
	var row api.HardwareFaultFeedbackRequest
	if err := s.db.First(&row, id).Error; err != nil {
		return row, err
	}
	start := row.FaultOccurredAt.Add(-time.Duration(row.PreWindowHours) * time.Hour)
	end := row.FaultOccurredAt.Add(time.Duration(row.PostWindowHours) * time.Hour)
	blockers := api.StringList{}
	needsHistoricalIdentity := row.IdentityResolutionStatus == "requires_historical_identity_at_fault_time" || strings.TrimSpace(row.GPUUUID) == ""
	if needsHistoricalIdentity {
		var interval api.HistoricalGPUIdentityInterval
		err := s.db.Where("node_ip = ? AND gpu_index = ? AND first_seen_at <= ? AND last_seen_at >= ?", row.NodeIP, row.GPUIndex, row.FaultOccurredAt, row.FaultOccurredAt).
			Order("observation_count DESC, last_seen_at DESC, id DESC").
			First(&interval).Error
		if err == nil {
			row.GPUUUID = interval.GPUUUID
			row.ModelName = firstNonEmpty(row.ModelName, interval.ModelName)
			row.IdentityResolutionStatus = "historical_identity_resolved"
			row.IdentityResolutionNote = fmt.Sprintf("resolved from historical identity interval #%d source=%s first=%s last=%s", interval.ID, interval.SourceKey, interval.FirstSeenAt.Format(time.RFC3339), interval.LastSeenAt.Format(time.RFC3339))
		} else if err == gorm.ErrRecordNotFound {
			row.IdentityResolutionStatus = "blocked_no_fault_time_identity"
			row.IdentityResolutionNote = "no historical GPU identity interval covers the reported fault time for this node and GPU index"
			blockers = append(blockers, "no historical GPU identity interval covers fault time")
		} else {
			return row, err
		}
	}
	if strings.TrimSpace(row.GPUUUID) == "" {
		blockers = append(blockers, "resolved failed GPU UUID is required before history pack extraction")
	}
	var audit api.MonitoringHistoryAudit
	err := s.db.Where("status = ? AND earliest_sample_at <= ? AND latest_sample_at >= ?", "success", start, end).
		Order("finished_at DESC, id DESC").
		First(&audit).Error
	if err == nil {
		if audit.SourceKey != "" && !strings.Contains(row.HistoryPackScope, "source_key=") {
			row.HistoryPackScope = row.HistoryPackScope + fmt.Sprintf(" source_key=%s", audit.SourceKey)
		}
	} else if err == gorm.ErrRecordNotFound {
		blockers = append(blockers, fmt.Sprintf("no successful historical monitoring audit covers %s to %s", start.Format(time.RFC3339), end.Format(time.RFC3339)))
	} else {
		return row, err
	}
	row.HistoryPackScope = feedbackHistoryScope(row.NodeIP, row.GPUUUID, row.ReportedGPUUUID, row.GPUIndex, row.FaultOccurredAt, row.PreWindowHours, row.PostWindowHours, row.IdentityResolutionStatus)
	if audit.SourceKey != "" {
		row.HistoryPackScope += fmt.Sprintf(" source_key=%s", audit.SourceKey)
	}
	if len(blockers) > 0 {
		row.Status = "blocked"
		row.HistoryPackStatus = "blocked_identity_unresolved"
		if row.IdentityResolutionStatus == "historical_identity_resolved" || row.IdentityResolutionStatus == "current_identity_selected" {
			row.HistoryPackStatus = "blocked_history_source_unavailable"
		}
		row.HistoryPackSHA256 = ""
		row.BlockingReasons = blockers
	} else {
		row.Status = "history_pack_manifest_ready"
		row.HistoryPackStatus = "manifest_ready_pending_metric_extraction"
		row.HistoryPackSHA256 = hardwareFeedbackPackChecksum(row, audit)
		row.BlockingReasons = api.StringList{}
	}
	if err := s.db.Save(&row).Error; err != nil {
		return row, err
	}
	return row, nil
}

func parseFeedbackTime(value string) (time.Time, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, fmt.Errorf("fault_occurred_at is required")
	}
	layouts := []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("fault_occurred_at must be RFC3339 or local datetime")
}

func hardwareFeedbackPackChecksum(row api.HardwareFaultFeedbackRequest, audit api.MonitoringHistoryAudit) string {
	fingerprint := map[string]any{
		"version":                    HardwareFaultFeedbackRequestVersion,
		"request_key":                row.RequestKey,
		"node_ip":                    row.NodeIP,
		"gpu_uuid":                   row.GPUUUID,
		"reported_gpu_uuid":          row.ReportedGPUUUID,
		"gpu_index":                  row.GPUIndex,
		"fault_type":                 row.FaultType,
		"fault_occurred_at":          row.FaultOccurredAt.UTC().Format(time.RFC3339),
		"pre_window_hours":           row.PreWindowHours,
		"post_window_hours":          row.PostWindowHours,
		"repair_action":              row.RepairAction,
		"hardware_replaced":          row.HardwareReplaced,
		"training_eligible":          row.TrainingEligible,
		"identity_resolution_status": row.IdentityResolutionStatus,
		"history_pack_scope":         row.HistoryPackScope,
		"source_key":                 audit.SourceKey,
		"audit_id":                   audit.ID,
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum)
}

func feedbackHistoryScope(nodeIP, gpuUUID, reportedGPUUUID string, gpuIndex int, occurredAt time.Time, preWindow, postWindow int, identityStatus string) string {
	start := occurredAt.Add(-time.Duration(preWindow) * time.Hour).Format(time.RFC3339)
	end := occurredAt.Add(time.Duration(postWindow) * time.Hour).Format(time.RFC3339)
	return fmt.Sprintf("node_ip=%s gpu_uuid=%s reported_gpu_uuid=%s gpu_index=%d identity_resolution=%s start=%s fault=%s end=%s", nodeIP, gpuUUID, reportedGPUUUID, gpuIndex, identityStatus, start, occurredAt.Format(time.RFC3339), end)
}

func warningReviewWindowHours(row api.HardwareFaultFeedbackRequest) int {
	if row.WarningReviewWindowHours > 0 {
		return row.WarningReviewWindowHours
	}
	if row.PreWindowHours > 0 {
		return row.PreWindowHours
	}
	return 24
}

func (s *Service) warningReviewThresholds(predictions []api.HardwareRiskPrediction) map[uint]float64 {
	specIDs := make([]uint, 0, len(predictions))
	seen := map[uint]bool{}
	for _, prediction := range predictions {
		if prediction.ModelSpecID == 0 || seen[prediction.ModelSpecID] {
			continue
		}
		seen[prediction.ModelSpecID] = true
		specIDs = append(specIDs, prediction.ModelSpecID)
	}
	if len(specIDs) == 0 {
		return map[uint]float64{}
	}
	var specs []api.PredictionModelSpec
	if err := s.db.Where("id IN ?", specIDs).Find(&specs).Error; err != nil {
		return map[uint]float64{}
	}
	thresholds := map[uint]float64{}
	for _, spec := range specs {
		if spec.DecisionThreshold != nil {
			thresholds[spec.ID] = *spec.DecisionThreshold
		}
	}
	return thresholds
}

func formatProbability(value *float64) string {
	if value == nil {
		return "nil"
	}
	return fmt.Sprintf("%.6f", *value)
}

func formatThreshold(value float64, ok bool) string {
	if !ok {
		return "nil"
	}
	return fmt.Sprintf("%.6f", value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
