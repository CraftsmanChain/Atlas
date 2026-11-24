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

		if isOneShotRule(hit.code) {
			if err := recordOneShotEvent(tx, asset, score, hit, episodeKey, observedAt, ruleVersion); err != nil {
				return err
			}
			continue
		}
		var event api.GPUFaultEvent
		err := tx.Where("episode_key = ? AND state = ?", episodeKey, "open").First(&event).Error
		if errors.Is(err, gorm.ErrRecordNotFound) && accumulatesAcrossRecoveries(hit.code) {
			err = tx.Where("episode_key = ?", episodeKey).Order("id DESC").First(&event).Error
			if err == nil {
				updates := map[string]any{
					"state": "open", "recovered_at": nil, "severity": hit.severity, "evidence": hit.evidence,
					"observed_value": hit.value, "threshold": hit.threshold, "occurrence_count": event.OccurrenceCount + 1,
					"latest_score_id": score.ID, "rule_version": ruleVersion, "last_observed_at": observedAt,
				}
				if updateErr := tx.Model(&event).Updates(updates).Error; updateErr != nil {
					return updateErr
				}
				continue
			}
		}
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

// XIDs are point events with no "recovered" signal, so their events are
// recorded already closed: state=recovered, recovered_at=first onset. While the
// rule keeps matching (the 24h onset window), hits update the latest record of
// the episode instead of creating a new one; a hit more than oneShotWindow after
// the last observation starts a new record.
const oneShotWindow = 24 * time.Hour

func isOneShotRule(ruleCode string) bool {
	return ruleCode == "recent_xid_change"
}

func recordOneShotEvent(tx *gorm.DB, asset api.GPUAsset, score api.GPUHealthScore, hit ruleHit, episodeKey string, observedAt time.Time, ruleVersion string) error {
	var event api.GPUFaultEvent
	err := tx.Where("episode_key = ? AND last_observed_at >= ?", episodeKey, observedAt.Add(-oneShotWindow)).Order("id DESC").First(&event).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		recoveredAt := observedAt
		event = api.GPUFaultEvent{
			EpisodeKey: episodeKey, Source: healthRuleEventSource, State: "recovered", RecoveredAt: &recoveredAt,
			GPUAssetID: asset.ID, GPUUUID: asset.CurrentUUID, NodeIP: asset.NodeIP, GPUIndex: asset.GPUIndex, ModelName: asset.ModelName,
			RuleCode: hit.code, Domain: hit.domain, Severity: hit.severity, Evidence: hit.evidence,
			ObservedValue: hit.value, Threshold: hit.threshold, OccurrenceCount: 1, LatestScoreID: score.ID,
			RuleVersion: ruleVersion, FirstObservedAt: observedAt, LastObservedAt: observedAt,
		}
		return tx.Create(&event).Error
	case err != nil:
		return err
	}
	updates := map[string]any{
		"severity": hit.severity, "evidence": hit.evidence, "observed_value": hit.value,
		"threshold": hit.threshold, "occurrence_count": event.OccurrenceCount + 1,
		"latest_score_id": score.ID, "rule_version": ruleVersion, "last_observed_at": observedAt,
	}
	if event.State != "recovered" || event.RecoveredAt == nil {
		recoveredAt := event.FirstObservedAt
		updates["state"], updates["recovered_at"] = "recovered", &recoveredAt
	}
	return tx.Model(&event).Updates(updates).Error
}

// closeOpenOneShotEvents closes XID events left open by earlier rule versions
// (including GPUs no longer evaluated: confidence D, swapped card, out of scope).
func closeOpenOneShotEvents(tx *gorm.DB) error {
	var events []api.GPUFaultEvent
	if err := tx.Where("source = ? AND state = ? AND rule_code = ?", healthRuleEventSource, "open", "recent_xid_change").Find(&events).Error; err != nil {
		return err
	}
	for _, event := range events {
		recoveredAt := event.FirstObservedAt
		if err := tx.Model(&event).Updates(map[string]any{"state": "recovered", "recovered_at": &recoveredAt}).Error; err != nil {
			return err
		}
	}
	return nil
}

func accumulatesAcrossRecoveries(ruleCode string) bool {
	return ruleCode == "gpu_temp_sustained_15m" || ruleCode == "gpu_temp_sustained_5m_critical"
}
