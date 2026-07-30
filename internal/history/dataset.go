package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"atlas/pkg/api"
)

const datasetBuildVersion = "gpu-fault-cohort-manifest-v1"

type DatasetBuildRequest struct {
	SourceKey string `json:"source_key"`
}

type datasetEpisode struct {
	EpisodeKey           string    `json:"episode_key"`
	CandidateIDs         []uint    `json:"candidate_ids"`
	SourceKey            string    `json:"source_key"`
	NodeIP               string    `json:"node_ip"`
	GPUUUID              string    `json:"gpu_uuid"`
	PCIBusID             string    `json:"pci_bus_id"`
	ModelName            string    `json:"model_name"`
	EventTypes           []string  `json:"event_types"`
	EventCodes           []string  `json:"event_codes"`
	QualityTier          string    `json:"quality_tier"`
	Eligibility          string    `json:"eligibility"`
	IdentityEvidence     string    `json:"identity_evidence"`
	SuccessorUUID        string    `json:"successor_uuid,omitempty"`
	LabelOnsetAt         time.Time `json:"label_onset_at"`
	LabelAvailableAt     time.Time `json:"label_available_at"`
	OriginalReviewStatus string    `json:"original_review_status"`
	TrainingDisposition  string    `json:"training_disposition"`
	RuleDecision         string    `json:"rule_decision"`
	RuleConfidence       float64   `json:"rule_confidence"`
	LabelSource          string    `json:"label_source"`
}

type datasetWindow struct {
	SampleKey        string    `json:"sample_key"`
	DatasetVersion   string    `json:"dataset_version"`
	EpisodeKey       string    `json:"episode_key"`
	CandidateIDs     []uint    `json:"candidate_ids"`
	NodeIP           string    `json:"node_ip"`
	GPUUUID          string    `json:"gpu_uuid"`
	PCIBusID         string    `json:"pci_bus_id"`
	ModelName        string    `json:"model_name"`
	EventTypes       []string  `json:"event_types"`
	QualityTier      string    `json:"quality_tier"`
	HorizonMinutes   int       `json:"horizon_minutes"`
	FeatureCutoffAt  time.Time `json:"feature_cutoff_at"`
	LabelOnsetAt     time.Time `json:"label_onset_at"`
	LabelAvailableAt time.Time `json:"label_available_at"`
	Eligibility      string    `json:"eligibility"`
	RuleDecision     string    `json:"rule_decision"`
	LabelSource      string    `json:"label_source"`
	LabelWeight      float64   `json:"label_weight"`
}

type datasetManifest struct {
	DatasetKey             string           `json:"dataset_key"`
	Version                string           `json:"version"`
	SourceKey              string           `json:"source_key"`
	CreatedAt              time.Time        `json:"created_at"`
	PointInTimeRule        string           `json:"point_in_time_rule"`
	FeaturePolicy          string           `json:"feature_policy"`
	LabelPolicy            string           `json:"label_policy"`
	HorizonsMinutes        []int            `json:"horizons_minutes"`
	CandidateCount         int              `json:"candidate_count"`
	EligibleCandidateCount int              `json:"eligible_candidate_count"`
	EpisodeCount           int              `json:"episode_count"`
	WindowCount            int              `json:"window_count"`
	WindowManifest         string           `json:"window_manifest"`
	WindowManifestSHA256   string           `json:"window_manifest_sha256"`
	Episodes               []datasetEpisode `json:"episodes"`
}

func (s *Service) DatasetBuilds(limit int) ([]api.TrainingDatasetBuild, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.TrainingDatasetBuild
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) BuildDatasetManifest(request DatasetBuildRequest) (api.TrainingDatasetBuild, error) {
	s.datasetMu.Lock()
	defer s.datasetMu.Unlock()

	source, err := s.resolveSource(request.SourceKey)
	if err != nil {
		return api.TrainingDatasetBuild{}, err
	}
	var identityRun api.HistoryBackfillRun
	result := s.db.Where("source_key = ? AND job_type = ? AND status = ?",
		source.ID, "gpu_identity_interval", "completed").
		Order("finished_at DESC, id DESC").Limit(1).Find(&identityRun)
	if result.Error != nil {
		return api.TrainingDatasetBuild{}, result.Error
	}
	if result.RowsAffected == 0 || identityRun.QueryVersion != identityBackfillQueryVersion {
		return api.TrainingDatasetBuild{}, fmt.Errorf(
			"completed identity backfill %s is required before dataset construction",
			identityBackfillQueryVersion,
		)
	}
	started := s.now()
	key := datasetBuildVersion + "-" + strconv.FormatInt(started.UTC().UnixNano(), 10)
	outputDir := filepath.Join(s.config.DatasetDir, "cohorts", key)
	build := api.TrainingDatasetBuild{
		DatasetKey: key, Version: datasetBuildVersion, Status: "running", SourceKey: source.ID,
		Horizons:  datasetHorizonLabels(CurrentTrainingCohortPolicy().PositiveHorizonsMinutes),
		OutputDir: outputDir, StartedAt: started,
	}
	if err := s.db.Create(&build).Error; err != nil {
		return build, err
	}
	if err := s.buildDatasetManifest(&build); err != nil {
		finished := s.now()
		_ = s.db.Model(&build).Updates(map[string]any{
			"status": "failed", "error_message": err.Error(), "finished_at": &finished,
		}).Error
		build.Status, build.ErrorMessage, build.FinishedAt = "failed", err.Error(), &finished
		return build, err
	}
	if err := s.db.First(&build, build.ID).Error; err != nil {
		return build, err
	}
	return build, nil
}

func (s *Service) buildDatasetManifest(build *api.TrainingDatasetBuild) error {
	var candidates []api.HistoricalFaultCandidate
	if err := s.db.Where("source_key = ?", build.SourceKey).
		Order("onset_at ASC, id ASC").Find(&candidates).Error; err != nil {
		return err
	}
	build.CandidateCount = len(candidates)
	episodesByKey := map[string]*datasetEpisode{}
	for _, candidate := range candidates {
		eligibility := candidateDatasetEligibility(candidate)
		switch eligibility {
		case "context_only":
			build.ContextOnlyCount++
			continue
		case "excluded":
			build.ExcludedCount++
			continue
		case "alert_identity_missing":
			build.IdentityMissingCount++
			continue
		case "pending_review":
			build.PendingReviewCount++
			continue
		}
		build.EligibleCandidateCount++
		episodeKey := datasetEpisodeKey(candidate)
		episode := episodesByKey[episodeKey]
		if episode == nil {
			episode = &datasetEpisode{
				EpisodeKey: episodeKey, SourceKey: candidate.SourceKey,
				NodeIP: candidate.NodeIP, GPUUUID: candidate.GPUUUID, PCIBusID: candidate.PCIBusID,
				ModelName: candidate.ModelName, QualityTier: candidate.QualityTier,
				Eligibility: eligibility, IdentityEvidence: candidate.IdentityEvidenceStatus,
				SuccessorUUID: candidate.IdentityEvidence["successor_uuid"],
				LabelOnsetAt:  candidate.OnsetAt, LabelAvailableAt: candidate.OnsetAt,
				OriginalReviewStatus: candidate.ReviewStatus,
				TrainingDisposition:  candidate.TrainingDisposition,
				RuleDecision:         candidate.RuleDecision, RuleConfidence: datasetLabelWeight(candidate),
				LabelSource: datasetLabelSource(candidate),
			}
			episodesByKey[episodeKey] = episode
		}
		episode.CandidateIDs = append(episode.CandidateIDs, candidate.ID)
		episode.EventTypes = appendUnique(episode.EventTypes, candidate.EventType)
		episode.EventCodes = appendUnique(episode.EventCodes, candidate.EventCode)
		if weight := datasetLabelWeight(candidate); weight > episode.RuleConfidence {
			episode.RuleConfidence = weight
		}
		if datasetLabelSource(candidate) == "human_override" {
			episode.LabelSource = "human_override"
		}
		if candidate.OnsetAt.Before(episode.LabelOnsetAt) {
			episode.LabelOnsetAt = candidate.OnsetAt
			episode.LabelAvailableAt = candidate.OnsetAt
		}
	}
	episodes := make([]datasetEpisode, 0, len(episodesByKey))
	for _, episode := range episodesByKey {
		sort.Slice(episode.CandidateIDs, func(i, j int) bool { return episode.CandidateIDs[i] < episode.CandidateIDs[j] })
		sort.Strings(episode.EventTypes)
		sort.Strings(episode.EventCodes)
		episodes = append(episodes, *episode)
	}
	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].LabelOnsetAt.Equal(episodes[j].LabelOnsetAt) {
			return episodes[i].EpisodeKey < episodes[j].EpisodeKey
		}
		return episodes[i].LabelOnsetAt.Before(episodes[j].LabelOnsetAt)
	})
	if err := os.MkdirAll(build.OutputDir, 0o750); err != nil {
		return fmt.Errorf("create dataset output directory: %w", err)
	}
	windowPath := filepath.Join(build.OutputDir, "sample_windows.jsonl")
	checksum, windowCount, err := writeDatasetWindows(windowPath, episodes)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(build.OutputDir, "manifest.json")
	manifest := datasetManifest{
		DatasetKey: build.DatasetKey, Version: datasetBuildVersion, SourceKey: build.SourceKey,
		CreatedAt: s.now(), HorizonsMinutes: CurrentTrainingCohortPolicy().PositiveHorizonsMinutes,
		PointInTimeRule: "feature_cutoff_at must be strictly earlier than label_onset_at",
		FeaturePolicy:   "this manifest contains extraction cutoffs only; no feature value or probability is fabricated",
		LabelPolicy:     "identity-supported and operator-accepted proxies are extractable but remain non-confirmed labels",
		CandidateCount:  build.CandidateCount, EligibleCandidateCount: build.EligibleCandidateCount,
		EpisodeCount: len(episodes), WindowCount: windowCount,
		WindowManifest: filepath.Base(windowPath), WindowManifestSHA256: checksum, Episodes: episodes,
	}
	if err := writeJSONAtomic(manifestPath, manifest); err != nil {
		return err
	}
	finished := s.now()
	return s.db.Model(build).Updates(map[string]any{
		"status": "completed", "candidate_count": build.CandidateCount,
		"eligible_candidate_count": build.EligibleCandidateCount, "episode_count": len(episodes),
		"window_count": windowCount, "pending_review_count": build.PendingReviewCount,
		"identity_missing_count": build.IdentityMissingCount, "context_only_count": build.ContextOnlyCount,
		"excluded_count": build.ExcludedCount, "manifest_path": manifestPath,
		"window_manifest_path": windowPath, "window_manifest_sha256": checksum,
		"finished_at": &finished,
	}).Error
}

func candidateDatasetEligibility(candidate api.HistoricalFaultCandidate) string {
	if candidate.ReviewStatus == "excluded" || candidate.TrainingDisposition == "excluded" {
		return "excluded"
	}
	if candidate.ReviewStatus == "accepted_proxy" {
		return "operator_accepted_proxy"
	}
	if candidate.TrainingDisposition == "context_only" || candidate.RuleDecision == "context_only" {
		return "context_only"
	}
	if candidate.RuleDecision == "needs_human_review" {
		if candidate.IdentityEvidenceStatus == "alert_identity_missing" {
			return "alert_identity_missing"
		}
		return "pending_review"
	}
	if candidate.RuleDecision == "positive_proxy" {
		return "rule_positive_proxy"
	}
	return "pending_review"
}

func datasetLabelSource(candidate api.HistoricalFaultCandidate) string {
	if candidate.ReviewStatus == "accepted_proxy" && candidate.ReviewedAt != nil {
		return "human_override"
	}
	return "versioned_rule"
}

func datasetLabelWeight(candidate api.HistoricalFaultCandidate) float64 {
	if candidate.ReviewStatus == "accepted_proxy" && candidate.ReviewedAt != nil {
		return 0.9
	}
	return candidate.RuleConfidence
}

func datasetEpisodeKey(candidate api.HistoricalFaultCandidate) string {
	identity := strconv.FormatUint(uint64(candidate.ID), 10)
	if candidate.IdentityEvidenceStatus == "replacement_after_event" {
		identity = strings.Join([]string{
			candidate.NodeIP, candidate.PCIBusID, candidate.IdentityEvidence["successor_uuid"],
		}, "|")
	}
	sum := sha256.Sum256([]byte(buildDatasetEpisodeKeyMaterial(candidate.SourceKey, identity)))
	return hex.EncodeToString(sum[:])
}

func buildDatasetEpisodeKeyMaterial(sourceKey, identity string) string {
	return datasetBuildVersion + "|" + sourceKey + "|" + identity
}

func datasetHorizonLabels(values []int) api.StringList {
	result := make(api.StringList, 0, len(values))
	for _, value := range values {
		result = append(result, strconv.Itoa(value)+"m")
	}
	return result
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func writeDatasetWindows(path string, episodes []datasetEpisode) (string, int, error) {
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", 0, fmt.Errorf("create sample-window manifest: %w", err)
	}
	checksum := sha256.New()
	writer := io.MultiWriter(file, checksum)
	encoder := json.NewEncoder(writer)
	count := 0
	for _, episode := range episodes {
		for _, horizon := range CurrentTrainingCohortPolicy().PositiveHorizonsMinutes {
			cutoff := episode.LabelOnsetAt.Add(-time.Duration(horizon) * time.Minute)
			row := datasetWindow{
				SampleKey:      datasetWindowKey(episode.EpisodeKey, horizon),
				DatasetVersion: datasetBuildVersion, EpisodeKey: episode.EpisodeKey,
				CandidateIDs: episode.CandidateIDs, NodeIP: episode.NodeIP, GPUUUID: episode.GPUUUID,
				PCIBusID: episode.PCIBusID, ModelName: episode.ModelName,
				EventTypes: episode.EventTypes, QualityTier: episode.QualityTier,
				HorizonMinutes: horizon, FeatureCutoffAt: cutoff,
				LabelOnsetAt: episode.LabelOnsetAt, LabelAvailableAt: episode.LabelAvailableAt,
				Eligibility: episode.Eligibility, RuleDecision: episode.RuleDecision,
				LabelSource: episode.LabelSource, LabelWeight: episode.RuleConfidence,
			}
			if err := encoder.Encode(row); err != nil {
				_ = file.Close()
				return "", count, fmt.Errorf("write sample-window manifest: %w", err)
			}
			count++
		}
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", count, err
	}
	if err := file.Close(); err != nil {
		return "", count, err
	}
	if err := os.Rename(temporary, path); err != nil {
		return "", count, fmt.Errorf("publish sample-window manifest: %w", err)
	}
	return hex.EncodeToString(checksum.Sum(nil)), count, nil
}

func datasetWindowKey(episodeKey string, horizon int) string {
	sum := sha256.Sum256([]byte(episodeKey + "|" + strconv.Itoa(horizon)))
	return hex.EncodeToString(sum[:])
}

func writeJSONAtomic(path string, value any) error {
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("create dataset manifest: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = file.Close()
		return fmt.Errorf("write dataset manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish dataset manifest: %w", err)
	}
	return nil
}
