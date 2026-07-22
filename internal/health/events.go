package health

import (
	"errors"
	"fmt"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

const healthRuleEventSource = "health_rule"

func reconcileFaultEvents(tx *gorm.DB, asset api.GPUAsset, score api.GPUHealthScore, hits []ruleHit, confidence string, observedAt time.Time, ruleVersion string) error {
	active := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		episodeKey := fmt.Sprintf("%s:%s", asset.CurrentUUID, hit.code)
		active[hit.code] = struct{}{}

		var event api.GPUFaultEvent
		err := tx.Where("episode_key = ? AND state = ?", episodeKey, "open").First(&event).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			event = api.GPUFaultEvent{
				EpisodeKey: episodeKey, Source: healthRuleEventSource, State: "open",
				GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, NodeIP: asset.NodeIP, GPUIndex: asset.GPUIndex, ModelName: asset.ModelName,
				RuleCode: hit.code, Domain: hit.domain, Severity: hit.severity, Evidence: hit.evidence,
				ObservedValue: hit.value, Threshold: hit.threshold, OccurrenceCount: 1, LatestScoreID: score.ID,
				RuleVersion: ruleVersion, FirstObservedAt: observedAt, LastObservedAt: observedAt,
			}
			if err := tx.Create(&event).Error; err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			updates := map[string]any{
				"severity": hit.severity, "evidence": hit.evidence, "observed_value": hit.value,
				"threshold": hit.threshold, "occurrence_count": event.OccurrenceCount + 1,
				"latest_score_id": score.ID, "rule_version": ruleVersion, "last_observed_at": observedAt,
			}
			if err := tx.Model(&event).Updates(updates).Error; err != nil {
				return err
			}
		}
	}

	// Missing telemetry is not evidence of recovery. Keep prior events open until
	// the GPU is observable again and the corresponding rule no longer matches.
	if confidence == "D" {
		return nil
	}
	var openEvents []api.GPUFaultEvent
	if err := tx.Where("gpu_uuid = ? AND source = ? AND state = ?", asset.CurrentUUID, healthRuleEventSource, "open").Find(&openEvents).Error; err != nil {
		return err
	}
	for _, event := range openEvents {
		if _, stillActive := active[event.RuleCode]; stillActive {
			continue
		}
		recoveredAt := observedAt
		if err := tx.Model(&event).Updates(map[string]any{"state": "recovered", "recovered_at": &recoveredAt}).Error; err != nil {
			return err
		}
	}
	return nil
}
