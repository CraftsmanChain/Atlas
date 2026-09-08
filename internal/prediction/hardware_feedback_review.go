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
	"gorm.io/gorm"
)

const (
	HardwareFaultFeedbackReviewVersion = "hardware-fault-feedback-review-v1"
	HardwareFaultEvidenceMatchVersion  = "hardware-fault-evidence-match-v1"
	HardwareFaultEpisodeVersion        = "hardware-fault-episode-v1"
	HardwareFaultValueReportVersion    = "hardware-fault-value-report-v1"
)

var hardwareFeedbackHorizons = []int{60, 360, 1440, 10080}

type HardwareFaultFeedbackReviewInput struct {
	Decision               string   `json:"decision"`
	Reviewer               string   `json:"reviewer"`
	ReviewNote             string   `json:"review_note"`
	ConfirmedOnsetAt       string   `json:"confirmed_onset_at"`
	ConfirmedWindowStartAt string   `json:"confirmed_window_start_at"`
	ConfirmedWindowEndAt   string   `json:"confirmed_window_end_at"`
	ConfirmedNodeIP        string   `json:"confirmed_node_ip"`
	ConfirmedGPUUUID       string   `json:"confirmed_gpu_uuid"`
	ConfirmedGPUIndex      *int     `json:"confirmed_gpu_index"`
	ConfirmedFaultType     string   `json:"confirmed_fault_type"`
	TargetScope            string   `json:"target_scope"`
	EpisodeKey             string   `json:"episode_key"`
	EvidenceKeys           []string `json:"evidence_keys"`
}

type HardwareFaultEvidenceMatch struct {
	CandidateKey     string            `json:"candidate_key"`
	SourceKey        string            `json:"source_key"`
	EventType        string            `json:"event_type"`
	EventCode        string            `json:"event_code,omitempty"`
	SourceMetric     string            `json:"source_metric"`
	NodeIP           string            `json:"node_ip,omitempty"`
	GPUUUID          string            `json:"gpu_uuid,omitempty"`
	OnsetAt          time.Time         `json:"onset_at"`
	DistanceSeconds  int64             `json:"distance_seconds"`
	IdentityStatus   string            `json:"identity_status,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	EvidenceSHA256   string            `json:"evidence_sha256"`
	NoActionExecuted bool              `json:"no_action_executed"`
}

type HardwareFaultEvidenceMatchReport struct {
	Version             string                       `json:"version"`
	FeedbackRequestID   uint                         `json:"feedback_request_id"`
	WindowStartAt       time.Time                    `json:"window_start_at"`
	WindowEndAt         time.Time                    `json:"window_end_at"`
	CandidateCount      int                          `json:"candidate_count"`
	IdentityBackfillGap bool                         `json:"identity_backfill_gap"`
	Candidates          []HardwareFaultEvidenceMatch `json:"candidates"`
	ReportSHA256        string                       `json:"report_sha256"`
	ReadOnly            bool                         `json:"read_only"`
	NoActionExecuted    bool                         `json:"no_action_executed"`
	GeneratedAt         time.Time                    `json:"generated_at"`
}

type HardwareFaultEpisode struct {
	EpisodeKey       string    `json:"episode_key"`
	Decision         string    `json:"decision"`
	TargetScope      string    `json:"target_scope"`
	FaultType        string    `json:"fault_type"`
	FeedbackIDs      []uint    `json:"feedback_ids"`
	EvidenceKeys     []string  `json:"evidence_keys"`
	TrainingEligible bool      `json:"training_eligible"`
	ReviewConflict   bool      `json:"review_conflict"`
	OnsetAt          time.Time `json:"onset_at"`
}

type HardwareFaultEpisodeReport struct {
	Version          string                 `json:"version"`
	FeedbackCount    int                    `json:"feedback_count"`
	ReviewedCount    int                    `json:"reviewed_count"`
	PendingCount     int                    `json:"pending_count"`
	EpisodeCount     int                    `json:"episode_count"`
	TrainingEpisodes int                    `json:"training_episodes"`
	Episodes         []HardwareFaultEpisode `json:"episodes"`
	ReportSHA256     string                 `json:"report_sha256"`
	GeneratedAt      time.Time              `json:"generated_at"`
}

type HardwareFaultHorizonValue struct {
	HorizonMinutes    int     `json:"horizon_minutes"`
	EligibleFaults    int     `json:"eligible_faults"`
	CoveredFaults     int     `json:"covered_faults"`
	WarningHits       int     `json:"warning_hits"`
	Recall            float64 `json:"recall_at_observed_threshold"`
	MedianLeadMinutes float64 `json:"median_lead_minutes"`
}

type HardwareFaultRankingValue struct {
	K              int     `json:"k"`
	EligibleFaults int     `json:"eligible_faults"`
	Hits           int     `json:"hits"`
	Recall         float64 `json:"recall_at_k"`
}

type HardwareFaultValueReport struct {
	Version              string                      `json:"version"`
	FrameworkVersion     string                      `json:"framework_version"`
	FeedbackCount        int                         `json:"feedback_count"`
	ReviewedCount        int                         `json:"reviewed_count"`
	ConfirmedCount       int                         `json:"confirmed_count"`
	ExcludedCount        int                         `json:"excluded_count"`
	ContextOnlyCount     int                         `json:"context_only_count"`
	PendingEvidenceCount int                         `json:"pending_evidence_count"`
	TrainingEpisodeCount int                         `json:"training_episode_count"`
	ByFaultType          map[string]int              `json:"by_fault_type"`
	ByTargetScope        map[string]int              `json:"by_target_scope"`
	Horizons             []HardwareFaultHorizonValue `json:"horizons"`
	RankingAtK           []HardwareFaultRankingValue `json:"ranking_at_k"`
	Interpretation       []string                    `json:"interpretation"`
	ReportSHA256         string                      `json:"report_sha256"`
	NoActionExecuted     bool                        `json:"no_action_executed"`
	GeneratedAt          time.Time                   `json:"generated_at"`
}

func (s *Service) HardwareFaultFeedbackReviews(feedbackID uint) ([]api.HardwareFaultFeedbackReview, error) {
	var rows []api.HardwareFaultFeedbackReview
	err := s.db.Where("feedback_request_id = ?", feedbackID).Order("revision ASC, id ASC").Find(&rows).Error
	return rows, err
}

func (s *Service) ReviewHardwareFaultFeedback(id uint, input HardwareFaultFeedbackReviewInput) (api.HardwareFaultFeedbackReview, error) {
	decision := strings.TrimSpace(input.Decision)
	switch decision {
	case "confirmed_hardware", "excluded_no_hardware", "context_only", "needs_evidence":
	default:
		return api.HardwareFaultFeedbackReview{}, fmt.Errorf("decision must be confirmed_hardware, excluded_no_hardware, context_only, or needs_evidence")
	}
	if strings.TrimSpace(input.Reviewer) == "" || strings.TrimSpace(input.ReviewNote) == "" {
		return api.HardwareFaultFeedbackReview{}, fmt.Errorf("reviewer and review_note are required")
	}
	var created api.HardwareFaultFeedbackReview
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var feedback api.HardwareFaultFeedbackRequest
		if err := tx.First(&feedback, id).Error; err != nil {
			return err
		}
		var previous api.HardwareFaultFeedbackReview
		result := tx.Where("feedback_request_id = ?", id).Order("revision DESC, id DESC").Limit(1).Find(&previous)
		if result.Error != nil {
			return result.Error
		}
		revision, previousSHA := 1, ""
		if result.RowsAffected > 0 {
			revision, previousSHA = previous.Revision+1, previous.ReviewSHA256
		}
		onset, err := reviewTime(input.ConfirmedOnsetAt, feedback.FaultOccurredAt)
		if err != nil {
			return fmt.Errorf("confirmed_onset_at: %w", err)
		}
		start, err := reviewOptionalTime(input.ConfirmedWindowStartAt)
		if err != nil {
			return fmt.Errorf("confirmed_window_start_at: %w", err)
		}
		end, err := reviewOptionalTime(input.ConfirmedWindowEndAt)
		if err != nil {
			return fmt.Errorf("confirmed_window_end_at: %w", err)
		}
		if start == nil {
			start = feedback.FaultWindowStartAt
		}
		if end == nil {
			end = feedback.FaultWindowEndAt
		}
		if start == nil {
			copy := onset
			start = &copy
		}
		if end == nil {
			copy := onset
			end = &copy
		}
		if end.Before(*start) || onset.Before(*start) || onset.After(*end) {
			return fmt.Errorf("confirmed onset must be inside the confirmed fault window")
		}
		scope := normalizeFeedbackTargetScope(firstNonEmpty(input.TargetScope, feedback.TargetScope))
		nodeIP := firstNonEmpty(input.ConfirmedNodeIP, feedback.NodeIP)
		gpuUUID := firstNonEmpty(input.ConfirmedGPUUUID, feedback.GPUUUID)
		gpuIndex := feedback.GPUIndex
		if input.ConfirmedGPUIndex != nil {
			gpuIndex = *input.ConfirmedGPUIndex
		}
		evidence := uniqueSorted(input.EvidenceKeys)
		trainingEligible := decision == "confirmed_hardware" && scope == "gpu"
		if decision == "confirmed_hardware" {
			if len(evidence) == 0 {
				return fmt.Errorf("confirmed_hardware requires at least one monitoring or repair evidence key")
			}
			if scope == "gpu" && strings.TrimSpace(gpuUUID) == "" {
				return fmt.Errorf("confirmed GPU hardware fault requires fault-time GPU UUID")
			}
			if scope == "gpu" {
				var identityCount int64
				if err := tx.Model(&api.HistoricalGPUIdentityInterval{}).Where("node_ip = ? AND gpu_uuid = ? AND first_seen_at <= ? AND last_seen_at >= ?", nodeIP, gpuUUID, onset, onset).Count(&identityCount).Error; err != nil {
					return err
				}
				if identityCount == 0 {
					return fmt.Errorf("fault-time GPU identity interval is missing; run identity backfill through the confirmed onset before confirming this record")
				}
			}
		}
		episodeKey := strings.TrimSpace(input.EpisodeKey)
		if episodeKey == "" {
			episodeKey = firstNonEmpty(feedback.SourceRecordID, feedback.RequestKey)
		}
		reviewedAt := s.now()
		created = api.HardwareFaultFeedbackReview{
			FeedbackRequestID: id, Revision: revision, Decision: decision, Reviewer: strings.TrimSpace(input.Reviewer), ReviewNote: strings.TrimSpace(input.ReviewNote),
			ConfirmedOnsetAt: &onset, ConfirmedWindowStartAt: start, ConfirmedWindowEndAt: end, ConfirmedNodeIP: nodeIP,
			ConfirmedGPUUUID: gpuUUID, ConfirmedGPUIndex: gpuIndex, ConfirmedFaultType: firstNonEmpty(input.ConfirmedFaultType, feedback.FaultType),
			TargetScope: scope, EpisodeKey: episodeKey, EvidenceKeys: api.StringList(evidence), TrainingEligible: trainingEligible,
			PreviousReviewSHA256: previousSHA, NoActionExecuted: true, ReviewedAt: reviewedAt,
		}
		created.ReviewSHA256 = feedbackReviewChecksum(created)
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		triage := decision
		// Only denormalized workflow fields are updated. Source/raw identity and
		// time fields remain immutable; corrected facts live on this review.
		updates := map[string]any{"triage_status": triage, "episode_key": episodeKey, "training_eligible": trainingEligible}
		return tx.Model(&feedback).Updates(updates).Error
	})
	return created, err
}

func (s *Service) HardwareFaultEvidenceMatches(id uint) (HardwareFaultEvidenceMatchReport, error) {
	var feedback api.HardwareFaultFeedbackRequest
	if err := s.db.First(&feedback, id).Error; err != nil {
		return HardwareFaultEvidenceMatchReport{}, err
	}
	onset := feedback.FaultOccurredAt
	if latest, ok, latestErr := s.latestHardwareFeedbackReview(id); latestErr != nil {
		return HardwareFaultEvidenceMatchReport{}, latestErr
	} else if ok && latest.ConfirmedOnsetAt != nil {
		onset = *latest.ConfirmedOnsetAt
	}
	start, end := onset.Add(-time.Duration(feedback.PreWindowHours)*time.Hour), onset.Add(time.Duration(feedback.PostWindowHours)*time.Hour)
	if feedback.FaultWindowStartAt != nil {
		start = feedback.FaultWindowStartAt.Add(-time.Duration(feedback.PreWindowHours) * time.Hour)
	}
	if feedback.FaultWindowEndAt != nil {
		end = feedback.FaultWindowEndAt.Add(time.Duration(feedback.PostWindowHours) * time.Hour)
	}
	query := s.db.Where("onset_at >= ? AND onset_at <= ?", start, end)
	if feedback.NodeIP != "" && feedback.GPUUUID != "" {
		query = query.Where("node_ip = ? OR gpu_uuid = ?", feedback.NodeIP, feedback.GPUUUID)
	} else if feedback.NodeIP != "" {
		query = query.Where("node_ip = ?", feedback.NodeIP)
	} else if feedback.GPUUUID != "" {
		query = query.Where("gpu_uuid = ?", feedback.GPUUUID)
	}
	var candidates []api.HistoricalFaultCandidate
	if err := query.Order("onset_at ASC, id ASC").Find(&candidates).Error; err != nil {
		return HardwareFaultEvidenceMatchReport{}, err
	}
	report := HardwareFaultEvidenceMatchReport{Version: HardwareFaultEvidenceMatchVersion, FeedbackRequestID: id, WindowStartAt: start, WindowEndAt: end, ReadOnly: true, NoActionExecuted: true, GeneratedAt: s.now(), Candidates: []HardwareFaultEvidenceMatch{}}
	for _, row := range candidates {
		match := HardwareFaultEvidenceMatch{CandidateKey: row.CandidateKey, SourceKey: row.SourceKey, EventType: row.EventType, EventCode: row.EventCode, SourceMetric: row.SourceMetric, NodeIP: row.NodeIP, GPUUUID: row.GPUUUID, OnsetAt: row.OnsetAt, DistanceSeconds: int64(row.OnsetAt.Sub(onset).Seconds()), IdentityStatus: row.IdentityEvidenceStatus, Labels: map[string]string(row.Labels), NoActionExecuted: true}
		match.EvidenceSHA256 = checksumJSON(match)
		report.Candidates = append(report.Candidates, match)
	}
	report.CandidateCount = len(report.Candidates)
	if feedback.TargetScope == "gpu" {
		var count int64
		_ = s.db.Model(&api.HistoricalGPUIdentityInterval{}).Where("node_ip = ? AND first_seen_at <= ? AND last_seen_at >= ?", feedback.NodeIP, feedback.FaultOccurredAt, feedback.FaultOccurredAt).Count(&count).Error
		report.IdentityBackfillGap = count == 0
	}
	report.ReportSHA256 = checksumJSON(struct {
		Version    string
		FeedbackID uint
		Start, End time.Time
		Candidates []HardwareFaultEvidenceMatch
		Gap        bool
	}{report.Version, id, start, end, report.Candidates, report.IdentityBackfillGap})
	return report, nil
}

func (s *Service) HardwareFaultEpisodes() (HardwareFaultEpisodeReport, error) {
	var feedback []api.HardwareFaultFeedbackRequest
	if err := s.db.Order("id ASC").Find(&feedback).Error; err != nil {
		return HardwareFaultEpisodeReport{}, err
	}
	latest, err := s.latestHardwareFeedbackReviews()
	if err != nil {
		return HardwareFaultEpisodeReport{}, err
	}
	report := HardwareFaultEpisodeReport{Version: HardwareFaultEpisodeVersion, FeedbackCount: len(feedback), GeneratedAt: s.now(), Episodes: []HardwareFaultEpisode{}}
	byEpisode := map[string]*HardwareFaultEpisode{}
	for _, row := range feedback {
		review, ok := latest[row.ID]
		if !ok {
			report.PendingCount++
			continue
		}
		report.ReviewedCount++
		key := firstNonEmpty(review.EpisodeKey, row.SourceRecordID, row.RequestKey)
		episode := byEpisode[key]
		if episode == nil {
			episode = &HardwareFaultEpisode{EpisodeKey: key, Decision: review.Decision, TargetScope: review.TargetScope, FaultType: review.ConfirmedFaultType, OnsetAt: derefTime(review.ConfirmedOnsetAt), TrainingEligible: review.TrainingEligible}
			byEpisode[key] = episode
		} else if episode.Decision != review.Decision {
			episode.Decision = "conflicting_reviews"
			episode.ReviewConflict = true
			episode.TrainingEligible = false
		}
		episode.FeedbackIDs = append(episode.FeedbackIDs, row.ID)
		episode.EvidenceKeys = uniqueSorted(append(episode.EvidenceKeys, []string(review.EvidenceKeys)...))
		if !episode.ReviewConflict {
			episode.TrainingEligible = episode.TrainingEligible && review.TrainingEligible
		}
		if onset := derefTime(review.ConfirmedOnsetAt); !onset.IsZero() && (episode.OnsetAt.IsZero() || onset.Before(episode.OnsetAt)) { episode.OnsetAt = onset }
	}
	keys := make([]string, 0, len(byEpisode))
	for key := range byEpisode {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		episode := *byEpisode[key]
		if episode.TrainingEligible {
			report.TrainingEpisodes++
		}
		report.Episodes = append(report.Episodes, episode)
	}
	report.EpisodeCount = len(report.Episodes)
	report.ReportSHA256 = checksumJSON(struct {
		Version  string
		Episodes []HardwareFaultEpisode
	}{report.Version, report.Episodes})
	return report, nil
}

func (s *Service) HardwareFaultValueReport() (HardwareFaultValueReport, error) {
	episodes, err := s.HardwareFaultEpisodes()
	if err != nil {
		return HardwareFaultValueReport{}, err
	}
	latest, err := s.latestHardwareFeedbackReviews()
	if err != nil {
		return HardwareFaultValueReport{}, err
	}
	report := HardwareFaultValueReport{Version: HardwareFaultValueReportVersion, FrameworkVersion: FrameworkVersion, FeedbackCount: episodes.FeedbackCount, ReviewedCount: episodes.ReviewedCount, TrainingEpisodeCount: episodes.TrainingEpisodes, ByFaultType: map[string]int{}, ByTargetScope: map[string]int{}, NoActionExecuted: true, GeneratedAt: s.now(), Interpretation: []string{"risk rankings are not calibrated failure probabilities", "recall and lead time use only immutable confirmed reviews and point-in-time shadow predictions", "small cohorts are descriptive evidence, not a release approval"}}
	confirmed := []api.HardwareFaultFeedbackReview{}
	for _, review := range latest {
		switch review.Decision {
		case "confirmed_hardware":
			report.ConfirmedCount++
			confirmed = append(confirmed, review)
			report.ByFaultType[review.ConfirmedFaultType]++
			report.ByTargetScope[review.TargetScope]++
		case "excluded_no_hardware":
			report.ExcludedCount++
		case "context_only":
			report.ContextOnlyCount++
		default:
			report.PendingEvidenceCount++
		}
	}
	confirmed = trainingEpisodeReviews(confirmed, episodes.Episodes)
	predictions, err := s.hardwareFaultValuePredictions(confirmed)
	if err != nil {
		return HardwareFaultValueReport{}, err
	}
	thresholds := s.warningReviewThresholds(predictions)
	for _, horizon := range hardwareFeedbackHorizons {
		value := HardwareFaultHorizonValue{HorizonMinutes: horizon}
		leads := []float64{}
		for _, review := range confirmed {
			if !review.TrainingEligible || review.ConfirmedOnsetAt == nil {
				continue
			}
			value.EligibleFaults++
			covered, hit, bestLead := false, false, -1.0
			for _, prediction := range predictions {
				if prediction.HorizonMinutes != horizon || !sameReviewPrediction(review, prediction) || prediction.EvaluatedAt.After(*review.ConfirmedOnsetAt) || prediction.ExpiresAt.Before(*review.ConfirmedOnsetAt) {
					continue
				}
				covered = true
				threshold, hasThreshold := thresholds[prediction.ModelSpecID]
				if hasThreshold && prediction.Probability != nil && *prediction.Probability >= threshold {
					hit = true
					lead := review.ConfirmedOnsetAt.Sub(prediction.EvaluatedAt).Minutes()
					if lead > bestLead {
						bestLead = lead
					}
				}
			}
			if covered {
				value.CoveredFaults++
			}
			if hit {
				value.WarningHits++
				leads = append(leads, bestLead)
			}
		}
		if value.EligibleFaults > 0 {
			value.Recall = float64(value.WarningHits) / float64(value.EligibleFaults)
		}
		value.MedianLeadMinutes = median(leads)
		report.Horizons = append(report.Horizons, value)
	}
	for _, k := range []int{5, 10, 20} {
		ranking := HardwareFaultRankingValue{K: k}
		for _, review := range confirmed {
			if !review.TrainingEligible || review.ConfirmedOnsetAt == nil {
				continue
			}
			eligible, hit := reviewRankingHit(review, predictions, k)
			if eligible {
				ranking.EligibleFaults++
				if hit {
					ranking.Hits++
				}
			}
		}
		if ranking.EligibleFaults > 0 {
			ranking.Recall = float64(ranking.Hits) / float64(ranking.EligibleFaults)
		}
		report.RankingAtK = append(report.RankingAtK, ranking)
	}
	report.ReportSHA256 = checksumJSON(struct {
		Version, Framework string
		Episodes           []HardwareFaultEpisode
		Horizons           []HardwareFaultHorizonValue
		Ranking            []HardwareFaultRankingValue
	}{report.Version, report.FrameworkVersion, episodes.Episodes, report.Horizons, report.RankingAtK})
	return report, nil
}

func trainingEpisodeReviews(reviews []api.HardwareFaultFeedbackReview, episodes []HardwareFaultEpisode) []api.HardwareFaultFeedbackReview {
	eligible := map[string]bool{}; for _, episode := range episodes { eligible[episode.EpisodeKey] = episode.TrainingEligible && !episode.ReviewConflict }
	selected := map[string]api.HardwareFaultFeedbackReview{}
	for _, review := range reviews {
		if !eligible[review.EpisodeKey] || !review.TrainingEligible { continue }
		current, ok := selected[review.EpisodeKey]
		if !ok || (review.ConfirmedOnsetAt != nil && (current.ConfirmedOnsetAt == nil || review.ConfirmedOnsetAt.Before(*current.ConfirmedOnsetAt))) { selected[review.EpisodeKey] = review }
	}
	keys := make([]string,0,len(selected)); for key := range selected { keys=append(keys,key) }; sort.Strings(keys)
	result := make([]api.HardwareFaultFeedbackReview,0,len(keys)); for _, key := range keys { result=append(result,selected[key]) }; return result
}

func (s *Service) hardwareFaultValuePredictions(reviews []api.HardwareFaultFeedbackReview) ([]api.HardwareRiskPrediction, error) {
	if len(reviews) == 0 {
		return []api.HardwareRiskPrediction{}, nil
	}
	var earliest, latest time.Time
	uuids, nodes := []string{}, []string{}
	for _, review := range reviews {
		if !review.TrainingEligible || review.ConfirmedOnsetAt == nil {
			continue
		}
		if earliest.IsZero() || review.ConfirmedOnsetAt.Before(earliest) {
			earliest = *review.ConfirmedOnsetAt
		}
		if latest.IsZero() || review.ConfirmedOnsetAt.After(latest) {
			latest = *review.ConfirmedOnsetAt
		}
		if review.ConfirmedGPUUUID != "" {
			uuids = append(uuids, review.ConfirmedGPUUUID)
		}
		if review.ConfirmedNodeIP != "" {
			nodes = append(nodes, review.ConfirmedNodeIP)
		}
	}
	if earliest.IsZero() {
		return []api.HardwareRiskPrediction{}, nil
	}
	var entityRows []api.HardwareRiskPrediction
	if err := s.db.Where("evaluated_at >= ? AND evaluated_at <= ? AND expires_at >= ?", earliest.Add(-7*24*time.Hour), latest, earliest).
		Where("gpu_uuid IN ? OR node_ip IN ?", uniqueSorted(uuids), uniqueSorted(nodes)).Order("evaluated_at ASC, id ASC").Find(&entityRows).Error; err != nil {
		return nil, err
	}
	runIDs := []uint{}
	for _, row := range entityRows {
		if row.ShadowRunID > 0 {
			runIDs = append(runIDs, row.ShadowRunID)
		}
	}
	if len(runIDs) == 0 {
		return entityRows, nil
	}
	var cohortRows []api.HardwareRiskPrediction
	if err := s.db.Where("shadow_run_id IN ?", uniqueSortedUint(runIDs)).Order("evaluated_at ASC, id ASC").Find(&cohortRows).Error; err != nil {
		return nil, err
	}
	return cohortRows, nil
}

func (s *Service) latestHardwareFeedbackReviews() (map[uint]api.HardwareFaultFeedbackReview, error) {
	var rows []api.HardwareFaultFeedbackReview
	if err := s.db.Order("feedback_request_id ASC, revision ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := map[uint]api.HardwareFaultFeedbackReview{}
	for _, row := range rows {
		result[row.FeedbackRequestID] = row
	}
	return result, nil
}

func (s *Service) latestHardwareFeedbackReview(feedbackID uint) (api.HardwareFaultFeedbackReview, bool, error) {
	var row api.HardwareFaultFeedbackReview
	result := s.db.Where("feedback_request_id = ?", feedbackID).Order("revision DESC, id DESC").Limit(1).Find(&row)
	return row, result.RowsAffected > 0, result.Error
}

func feedbackReviewChecksum(review api.HardwareFaultFeedbackReview) string {
	review.ID = 0
	review.ReviewSHA256 = ""
	review.CreatedAt = time.Time{}
	return checksumJSON(review)
}
func checksumJSON(value any) string {
	payload, _ := json.Marshal(value)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
func reviewTime(value string, fallback time.Time) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	return parseFeedbackTime(value)
}
func reviewOptionalTime(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := parseFeedbackTime(value)
	return &parsed, err
}
func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
func sameReviewPrediction(review api.HardwareFaultFeedbackReview, prediction api.HardwareRiskPrediction) bool {
	if review.ConfirmedGPUUUID != "" && prediction.GPUUUID != "" {
		return review.ConfirmedGPUUUID == prediction.GPUUUID
	}
	return review.ConfirmedNodeIP != "" && review.ConfirmedNodeIP == prediction.NodeIP
}
func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	mid := len(values) / 2
	if len(values)%2 == 1 {
		return values[mid]
	}
	return (values[mid-1] + values[mid]) / 2
}

func reviewRankingHit(review api.HardwareFaultFeedbackReview, predictions []api.HardwareRiskPrediction, k int) (bool, bool) {
	if review.ConfirmedOnsetAt == nil {
		return false, false
	}
	type cohortKey struct {
		RunID, ModelID uint
		Horizon        int
		Evaluated      int64
	}
	cohorts := map[cohortKey][]api.HardwareRiskPrediction{}
	for _, prediction := range predictions {
		if prediction.ShadowRunID == 0 || prediction.Probability == nil || prediction.EvaluatedAt.After(*review.ConfirmedOnsetAt) || prediction.ExpiresAt.Before(*review.ConfirmedOnsetAt) {
			continue
		}
		key := cohortKey{prediction.ShadowRunID, prediction.ModelSpecID, prediction.HorizonMinutes, prediction.EvaluatedAt.Unix()}
		cohorts[key] = append(cohorts[key], prediction)
	}
	eligible, hit := false, false
	for _, cohort := range cohorts {
		contains := false
		for _, prediction := range cohort {
			if sameReviewPrediction(review, prediction) {
				contains = true
				break
			}
		}
		if !contains {
			continue
		}
		eligible = true
		sort.SliceStable(cohort, func(i, j int) bool { return *cohort[i].Probability > *cohort[j].Probability })
		limit := k
		if limit > len(cohort) {
			limit = len(cohort)
		}
		for _, prediction := range cohort[:limit] {
			if sameReviewPrediction(review, prediction) {
				hit = true
				break
			}
		}
		if hit {
			break
		}
	}
	return eligible, hit
}

func uniqueSortedUint(values []uint) []uint {
	seen := map[uint]struct{}{}
	result := []uint{}
	for _, value := range values {
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
