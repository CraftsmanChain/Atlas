package prediction

import (
	"fmt"
	"strings"
	"time"

	"atlas/pkg/api"
)

const HardwareFaultFeedbackRequestVersion = "hardware-fault-feedback-request-v1"

type HardwareFaultFeedbackInput struct {
	NodeIP           string `json:"node_ip"`
	GPUUUID          string `json:"gpu_uuid"`
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
	faultType := strings.TrimSpace(input.FaultType)
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
	row := api.HardwareFaultFeedbackRequest{
		RequestKey:        fmt.Sprintf("fault-feedback-%d", now.UnixNano()),
		Status:            "history_pack_requested",
		NodeIP:            nodeIP,
		GPUUUID:           gpuUUID,
		GPUIndex:          input.GPUIndex,
		GPUAssetID:        input.GPUAssetID,
		FaultType:         faultType,
		FaultOccurredAt:   occurredAt,
		PreWindowHours:    preWindow,
		PostWindowHours:   postWindow,
		Operator:          operator,
		Description:       strings.TrimSpace(input.Description),
		RepairAction:      strings.TrimSpace(input.RepairAction),
		HardwareReplaced:  input.HardwareReplaced,
		EvidenceNote:      strings.TrimSpace(input.EvidenceNote),
		TrainingEligible:  input.TrainingEligible,
		HistoryPackStatus: "queued_offline_collection",
		HistoryPackScope:  feedbackHistoryScope(nodeIP, gpuUUID, input.GPUIndex, occurredAt, preWindow, postWindow),
		BlockingReasons:   api.StringList{"offline pre/post monitoring history pack has not been collected yet"},
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
		row.GPUUUID = firstNonEmpty(row.GPUUUID, asset.CurrentUUID)
		row.GPUIndex = asset.GPUIndex
		row.ModelName = firstNonEmpty(asset.ModelName, asset.Model)
	}
	if err := s.db.Create(&row).Error; err != nil {
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

func feedbackHistoryScope(nodeIP, gpuUUID string, gpuIndex int, occurredAt time.Time, preWindow, postWindow int) string {
	start := occurredAt.Add(-time.Duration(preWindow) * time.Hour).Format(time.RFC3339)
	end := occurredAt.Add(time.Duration(postWindow) * time.Hour).Format(time.RFC3339)
	return fmt.Sprintf("node_ip=%s gpu_uuid=%s gpu_index=%d start=%s fault=%s end=%s", nodeIP, gpuUUID, gpuIndex, start, occurredAt.Format(time.RFC3339), end)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
