package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"atlas/internal/features"
	promclient "atlas/internal/prometheus"
	"atlas/pkg/api"
)

const manualFeedbackFeatureRequestVersion = "manual-feedback-feature-request-v1"
const manualFeedbackSourceManifestVersion = "prediction-human-feedback-manifest-v1"

type ManualFeedbackFeatureRequestBuildRequest struct {
	SourceKey string `json:"source_key,omitempty"`
}

type ManualFeedbackFeatureRequestWorkerRequest struct {
	BuildID uint `json:"build_id,omitempty"`
}

type manualFeedbackFeatureManifest struct {
	RequestKey             string                                `json:"request_key"`
	Version                string                                `json:"version"`
	SourceKey              string                                `json:"source_key"`
	SourceManifestVersion  string                                `json:"source_manifest_version"`
	SourceManifestSHA256   string                                `json:"source_manifest_sha256"`
	FeatureContractVersion string                                `json:"feature_contract_version"`
	PointInTimeRule        string                                `json:"point_in_time_rule"`
	ExecutionPolicy        string                                `json:"execution_policy"`
	TelemetryPolicy        string                                `json:"telemetry_policy"`
	LookbackMinutes        int                                   `json:"lookback_minutes"`
	QueryStepSeconds       int                                   `json:"query_step_seconds"`
	MetricFamilies         []string                              `json:"metric_families"`
	WindowCount            int                                   `json:"window_count"`
	Records                []manualFeedbackFeatureManifestRecord `json:"records"`
	CreatedAt              time.Time                             `json:"created_at"`
}

type manualFeedbackFeatureManifestRecord struct {
	FeedbackRequestID    uint      `json:"feedback_request_id"`
	RequestKey           string    `json:"feedback_request_key"`
	NodeIP               string    `json:"node_ip"`
	TargetScope          string    `json:"target_scope"`
	GPUUUID              string    `json:"gpu_uuid"`
	ReportedGPUUUID      string    `json:"reported_gpu_uuid,omitempty"`
	GPUIndex             int       `json:"gpu_index"`
	AffectedGPUIndexes   []string  `json:"affected_gpu_indexes,omitempty"`
	ModelName            string    `json:"model_name,omitempty"`
	FaultType            string    `json:"fault_type"`
	FaultOccurredAt      time.Time `json:"fault_occurred_at"`
	FaultTimePrecision   string    `json:"fault_time_precision"`
	FaultWindowStartAt   time.Time `json:"fault_window_start_at"`
	FaultWindowEndAt     time.Time `json:"fault_window_end_at"`
	LabelAvailableAt     time.Time `json:"label_available_at"`
	PreWindowStartAt     time.Time `json:"pre_window_start_at"`
	PostWindowEndAt      time.Time `json:"post_window_end_at"`
	FeatureCutoffAt      time.Time `json:"feature_cutoff_at"`
	PreWindowHours       int       `json:"pre_window_hours"`
	PostWindowHours      int       `json:"post_window_hours"`
	HistoryPackSHA256    string    `json:"history_pack_sha256"`
	HistoryPackScope     string    `json:"history_pack_scope"`
	WarningReviewStatus  string    `json:"warning_review_status"`
	MatchedWarningCount  int       `json:"matched_warning_count"`
	RepairAction         string    `json:"repair_action,omitempty"`
	TrainingEligible     bool      `json:"training_eligible"`
	IdentityStatus       string    `json:"identity_resolution_status"`
	NoAlertEmitted       bool      `json:"no_alert_emitted"`
	NoActionExecuted     bool      `json:"no_action_executed"`
	NoRawTelemetryStored bool      `json:"no_raw_telemetry_stored"`
}

func (s *Service) ManualFeedbackFeatureRequestBuilds(limit int) ([]api.ManualFeedbackFeatureRequestBuild, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var rows []api.ManualFeedbackFeatureRequestBuild
	err := s.db.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) BuildManualFeedbackFeatureRequestManifest(request ManualFeedbackFeatureRequestBuildRequest) (api.ManualFeedbackFeatureRequestBuild, error) {
	started := s.now()
	sourceKey := strings.TrimSpace(request.SourceKey)
	manifestSHA, err := s.currentHumanFeedbackManifestSHA()
	if err != nil {
		return api.ManualFeedbackFeatureRequestBuild{}, err
	}
	var feedback []api.HardwareFaultFeedbackRequest
	if err := s.db.Order("fault_occurred_at ASC, id ASC").Find(&feedback).Error; err != nil {
		return api.ManualFeedbackFeatureRequestBuild{}, err
	}
	records := make([]manualFeedbackFeatureManifestRecord, 0, len(feedback))
	build := api.ManualFeedbackFeatureRequestBuild{
		RequestKey:             fmt.Sprintf("manual-feedback-features-%d", started.UnixNano()),
		Version:                manualFeedbackFeatureRequestVersion,
		Status:                 "blocked",
		SourceManifestVersion:  manualFeedbackSourceManifestVersion,
		SourceManifestSHA256:   manifestSHA,
		FeatureContractVersion: features.CatalogVersion,
		LookbackMinutes:        int(featureLookback / time.Minute),
		QueryStepSeconds:       int(featureQueryStep / time.Second),
		MetricFamilies:         ResearchMetricFamilies(),
		MetricFamilyCount:      len(ResearchMetricFamilies()),
		NoRawTelemetryStored:   true,
		NoAlertEmitted:         true,
		NoActionExecuted:       true,
		StartedAt:              started,
	}
	blockers := api.StringList{}
	for _, row := range feedback {
		build.HardwareFeedbackRequests++
		if row.TrainingEligible {
			build.TrainingEligibleRequests++
		}
		if row.HistoryPackStatus == "manifest_ready_pending_metric_extraction" {
			build.PackReadyRequests++
		}
		if isManualFeedbackWarningReviewed(row.WarningReviewStatus) {
			build.WarningReviewedRequests++
		}
		if row.WarningReviewStatus == "manual_feedback_no_prior_shadow_warning" {
			build.WarningMissRequests++
		}
		rowBlockers := manualFeedbackFeatureBlockers(row)
		if len(rowBlockers) > 0 {
			build.BlockedRequests++
			blockers = append(blockers, rowBlockers...)
			continue
		}
		rowSource := sourceKey
		if rowSource == "" {
			rowSource = sourceKeyFromHistoryPackScope(row.HistoryPackScope)
		}
		if rowSource == "" {
			build.BlockedRequests++
			blockers = append(blockers, fmt.Sprintf("feedback %d missing history source_key", row.ID))
			continue
		}
		if sourceKey == "" {
			sourceKey = rowSource
		}
		if rowSource != sourceKey {
			build.BlockedRequests++
			blockers = append(blockers, fmt.Sprintf("feedback %d source_key %s does not match build source_key %s", row.ID, rowSource, sourceKey))
			continue
		}
		faultStart, faultEnd := manualFeedbackFaultWindow(row)
		records = append(records, manualFeedbackFeatureManifestRecord{
			FeedbackRequestID: row.ID, RequestKey: row.RequestKey, NodeIP: row.NodeIP, TargetScope: manualFeedbackTargetScope(row), GPUUUID: row.GPUUUID,
			ReportedGPUUUID: row.ReportedGPUUUID, GPUIndex: row.GPUIndex, AffectedGPUIndexes: append([]string(nil), row.AffectedGPUIndexes...), ModelName: row.ModelName, FaultType: row.FaultType,
			FaultOccurredAt: row.FaultOccurredAt, FaultTimePrecision: manualFeedbackTimePrecision(row), FaultWindowStartAt: faultStart, FaultWindowEndAt: faultEnd, LabelAvailableAt: row.CreatedAt,
			PreWindowStartAt: faultStart.Add(-time.Duration(row.PreWindowHours) * time.Hour),
			PostWindowEndAt:  faultEnd.Add(time.Duration(row.PostWindowHours) * time.Hour),
			FeatureCutoffAt:  faultStart, PreWindowHours: row.PreWindowHours, PostWindowHours: row.PostWindowHours,
			HistoryPackSHA256: row.HistoryPackSHA256, HistoryPackScope: row.HistoryPackScope,
			WarningReviewStatus: row.WarningReviewStatus, MatchedWarningCount: row.MatchedWarningCount,
			RepairAction: row.RepairAction, TrainingEligible: row.TrainingEligible, IdentityStatus: row.IdentityResolutionStatus,
			NoAlertEmitted: true, NoActionExecuted: true, NoRawTelemetryStored: true,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].FaultOccurredAt.Equal(records[j].FaultOccurredAt) {
			return records[i].FeedbackRequestID < records[j].FeedbackRequestID
		}
		return records[i].FaultOccurredAt.Before(records[j].FaultOccurredAt)
	})
	build.SourceKey = sourceKey
	build.WindowCount = len(records)
	build.BlockingReasons = api.StringList(uniqueSortedStrings(blockers))
	if build.SourceKey == "" {
		build.BlockingReasons = api.StringList(uniqueSortedStrings(append([]string(build.BlockingReasons), "no history source_key resolved from feedback packs")))
	}
	if build.WindowCount == 0 {
		build.BlockingReasons = api.StringList(uniqueSortedStrings(append([]string(build.BlockingReasons), "no training-eligible pack-ready reviewed manual feedback is available")))
	}
	if len(build.BlockingReasons) == 0 {
		build.Status = "manifest_ready_pending_offline_worker"
	}
	build.OutputDir = filepath.Join(s.config.DatasetDir, "manual-feedback-feature-requests", build.RequestKey)
	if err := s.db.Create(&build).Error; err != nil {
		return build, err
	}
	if err := os.MkdirAll(build.OutputDir, 0o750); err != nil {
		build.ErrorMessage = err.Error()
		_ = s.db.Save(&build).Error
		return build, err
	}
	manifestPath := filepath.Join(build.OutputDir, "manifest.json")
	manifest := manualFeedbackFeatureManifest{
		RequestKey: build.RequestKey, Version: build.Version, SourceKey: build.SourceKey,
		SourceManifestVersion: build.SourceManifestVersion, SourceManifestSHA256: build.SourceManifestSHA256,
		FeatureContractVersion: build.FeatureContractVersion,
		PointInTimeRule:        "feature_cutoff_at equals the reported fault time; all feature samples must be strictly earlier than or equal to feature_cutoff_at and never later than the label availability time",
		ExecutionPolicy:        "offline worker only; read-only monitoring source access; no repair, scheduling, isolation, alert, or workload action",
		TelemetryPolicy:        "manifest stores request metadata and future aggregate feature paths only; raw multi-year telemetry remains in the monitoring source",
		LookbackMinutes:        build.LookbackMinutes, QueryStepSeconds: build.QueryStepSeconds,
		MetricFamilies: append([]string(nil), build.MetricFamilies...), WindowCount: build.WindowCount, Records: records, CreatedAt: started,
	}
	manifestChecksum, err := writeManualFeedbackFeatureManifest(manifestPath, manifest)
	if err != nil {
		build.ErrorMessage = err.Error()
		_ = s.db.Save(&build).Error
		return build, err
	}
	finished := s.now()
	if err := s.db.Model(&build).Updates(map[string]any{
		"status": build.Status, "source_key": build.SourceKey, "window_count": build.WindowCount,
		"blocked_requests": build.BlockedRequests, "blocking_reasons": build.BlockingReasons,
		"manifest_path": manifestPath, "manifest_sha256": manifestChecksum, "finished_at": &finished,
	}).Error; err != nil {
		return build, err
	}
	build.ManifestPath = manifestPath
	build.ManifestSHA256 = manifestChecksum
	build.FinishedAt = &finished
	return build, nil
}

func (s *Service) StartManualFeedbackFeatureRequestWorker(request ManualFeedbackFeatureRequestWorkerRequest) (api.ManualFeedbackFeatureRequestBuild, error) {
	s.manualFeedbackFeatureMu.Lock()
	defer s.manualFeedbackFeatureMu.Unlock()
	if s.manualFeedbackFeatureRunning {
		return api.ManualFeedbackFeatureRequestBuild{}, fmt.Errorf("a manual feedback feature aggregation is already running")
	}
	var build api.ManualFeedbackFeatureRequestBuild
	query := s.db.Where("status = ? AND version = ?", "manifest_ready_pending_offline_worker", manualFeedbackFeatureRequestVersion)
	if request.BuildID > 0 {
		query = query.Where("id = ?", request.BuildID)
	}
	result := query.Order("created_at DESC, id DESC").Limit(1).Find(&build)
	if result.Error != nil {
		return api.ManualFeedbackFeatureRequestBuild{}, result.Error
	}
	if result.RowsAffected == 0 {
		return api.ManualFeedbackFeatureRequestBuild{}, fmt.Errorf("a manifest_ready_pending_offline_worker manual feedback feature request is required")
	}
	if build.ManifestPath == "" || build.ManifestSHA256 == "" {
		return build, fmt.Errorf("manual feedback feature request manifest is missing")
	}
	if _, err := s.resolveSource(build.SourceKey); err != nil {
		return build, err
	}
	started := s.now()
	if err := s.db.Model(&build).Updates(map[string]any{
		"status": "queued", "started_at": started, "finished_at": nil, "error_message": "",
	}).Error; err != nil {
		return build, err
	}
	build.Status = "queued"
	build.StartedAt = started
	build.FinishedAt = nil
	build.ErrorMessage = ""
	s.manualFeedbackFeatureRunning = true
	go s.executeManualFeedbackFeatureRequestWorker(build.ID)
	return build, nil
}

func (s *Service) executeManualFeedbackFeatureRequestWorker(buildID uint) {
	defer func() {
		s.manualFeedbackFeatureMu.Lock()
		s.manualFeedbackFeatureRunning = false
		s.manualFeedbackFeatureMu.Unlock()
	}()
	var build api.ManualFeedbackFeatureRequestBuild
	if err := s.db.First(&build, buildID).Error; err != nil {
		return
	}
	if err := s.db.Model(&build).Update("status", "running").Error; err != nil {
		return
	}
	if err := s.aggregateManualFeedbackFeatures(&build); err != nil {
		finished := s.now()
		_ = s.db.Model(&build).Updates(map[string]any{
			"status": "failed", "error_message": err.Error(), "finished_at": &finished,
		}).Error
	}
}

func (s *Service) aggregateManualFeedbackFeatures(build *api.ManualFeedbackFeatureRequestBuild) error {
	if err := verifyFileSHA256(build.ManifestPath, build.ManifestSHA256); err != nil {
		return fmt.Errorf("manual feedback feature request checksum: %w", err)
	}
	manifest, err := readManualFeedbackFeatureManifest(build.ManifestPath)
	if err != nil {
		return err
	}
	if manifest.RequestKey != build.RequestKey {
		return fmt.Errorf("manifest request_key %q does not match build request_key %q", manifest.RequestKey, build.RequestKey)
	}
	if manifest.SourceKey != build.SourceKey {
		return fmt.Errorf("manifest source_key %q does not match build source_key %q", manifest.SourceKey, build.SourceKey)
	}
	if len(manifest.Records) == 0 {
		return fmt.Errorf("manual feedback feature request has no records")
	}
	if err := os.MkdirAll(build.OutputDir, 0o750); err != nil {
		return fmt.Errorf("create manual feedback feature directory: %w", err)
	}
	source, err := s.resolveSource(build.SourceKey)
	if err != nil {
		return err
	}
	client, err := s.historyClient(source)
	if err != nil {
		return err
	}
	featureBuild := api.TrainingFeatureBuild{
		FeatureDatasetKey: build.RequestKey, Version: featureDatasetVersion,
		SourceKey: build.SourceKey, SourceDatasetKey: manifest.SourceManifestSHA256,
		FeatureContractVersion: build.FeatureContractVersion,
		LookbackMinutes:        build.LookbackMinutes, QueryStepSeconds: build.QueryStepSeconds,
		EpisodeCount: len(manifest.Records), WindowCount: len(manifest.Records),
	}
	rows := make([]extractedFeatureRow, 0, len(manifest.Records))
	failedWindows := 0
	for index, record := range manifest.Records {
		row, queryErr := s.extractManualFeedbackFeatureRecord(client, &featureBuild, record)
		if queryErr != nil {
			failedWindows++
		}
		rows = append(rows, row)
		if (index+1)%10 == 0 || index+1 == len(manifest.Records) {
			_ = s.db.Model(build).Updates(map[string]any{
				"completed_windows": len(rows) - failedWindows,
				"failed_windows":    failedWindows,
			}).Error
		}
	}
	featurePath := filepath.Join(build.OutputDir, "features.jsonl")
	checksum, err := writeFeatureRows(featurePath, rows)
	if err != nil {
		return err
	}
	report := buildFeatureQualityReport(featureBuild, rows)
	report.FeatureDatasetKey = build.RequestKey
	report.Version = manualFeedbackFeatureRequestVersion
	report.SourceDatasetKey = manifest.SourceManifestSHA256
	report.PointInTimeRule = "manual feedback aggregation uses only samples at or before feature_cutoff_at; post-fault windows remain evidence context and are not used as training features"
	reportPath := filepath.Join(build.OutputDir, "quality_report.json")
	if err := writeJSONAtomic(reportPath, report); err != nil {
		return err
	}
	status, errorMessage := "features_ready_pending_training_preparation", ""
	if report.FailedWindows > 0 {
		status = "features_ready_with_errors_pending_training_preparation"
	}
	if report.CompletedWindows == 0 && report.WindowCount > 0 {
		status = "failed"
		errorMessage = "all manual feedback Prometheus feature queries failed; inspect extraction_error in features.jsonl"
	}
	finished := s.now()
	return s.db.Model(build).Updates(map[string]any{
		"status": status, "completed_windows": report.CompletedWindows, "failed_windows": report.FailedWindows,
		"feature_column_count":    report.FeatureColumnCount,
		"average_metric_coverage": report.AverageCoverage, "minimum_metric_coverage": report.MinimumCoverage,
		"feature_path": featurePath, "feature_sha256": checksum, "quality_report_path": reportPath,
		"no_raw_telemetry_stored": true, "no_alert_emitted": true, "no_action_executed": true,
		"error_message": errorMessage, "finished_at": &finished,
	}).Error
}

func (s *Service) extractManualFeedbackFeatureRecord(client *promclient.Client, build *api.TrainingFeatureBuild, record manualFeedbackFeatureManifestRecord) (extractedFeatureRow, error) {
	window := manualFeedbackFeatureWindow(record)
	query := historicalMetricQuery(record.GPUUUID)
	if strings.TrimSpace(record.GPUUUID) == "" {
		query = manualFeedbackNodeMetricQuery(record.NodeIP)
	}
	if strings.TrimSpace(query) == "" {
		err := fmt.Errorf("manual feedback record %d missing GPU UUID and node IP", record.FeedbackRequestID)
		return emptyExtractedFeatureRow(build, window, err.Error()), err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	series, err := client.QueryRange(ctx, query, window.FeatureCutoffAt.Add(-featureLookback), window.FeatureCutoffAt, featureQueryStep)
	cancel()
	if err != nil {
		return emptyExtractedFeatureRow(build, window, err.Error()), err
	}
	row := summarizeFeatureWindow(build, window, canonicalSeries(series))
	if strings.TrimSpace(record.GPUUUID) == "" {
		row.GPUUUID = "node:" + record.NodeIP
	}
	return row, nil
}

func manualFeedbackFeatureWindow(record manualFeedbackFeatureManifestRecord) datasetWindow {
	entity := strings.TrimSpace(record.GPUUUID)
	if entity == "" {
		entity = "node:" + record.NodeIP
	}
	return datasetWindow{
		SampleKey:        fmt.Sprintf("manual-feedback-%d", record.FeedbackRequestID),
		DatasetVersion:   manualFeedbackFeatureRequestVersion,
		EpisodeKey:       record.RequestKey,
		NodeIP:           record.NodeIP,
		GPUUUID:          entity,
		ModelName:        record.ModelName,
		HorizonMinutes:   0,
		FeatureCutoffAt:  record.FeatureCutoffAt,
		LabelOnsetAt:     record.FaultWindowStartAt,
		LabelAvailableAt: record.LabelAvailableAt,
		LabelWeight:      1,
		Eligibility:      "manual_feedback_confirmed_hardware",
		RuleDecision:     record.WarningReviewStatus,
		LabelSource:      "manual_hardware_fault_feedback",
	}
}

func manualFeedbackNodeMetricQuery(nodeIP string) string {
	nodeIP = strings.TrimSpace(nodeIP)
	if nodeIP == "" {
		return ""
	}
	names := make([]string, 0, len(historicalFeatureMetrics))
	for _, metric := range historicalFeatureMetrics {
		names = append(names, regexp.QuoteMeta(metric.Name))
	}
	sort.Strings(names)
	metricExpression := "^(" + strings.Join(names, "|") + ")$"
	ip := regexp.QuoteMeta(nodeIP)
	instanceExpression := "(?i)^" + ip + "(:[0-9]+)?$"
	hostExpression := "(?i)^" + ip + "$"
	return fmt.Sprintf(`{__name__=~%q,instance=~%q} or {__name__=~%q,Instance=~%q} or {__name__=~%q,node_ip=~%q} or {__name__=~%q,ip=~%q} or {__name__=~%q,host=~%q} or {__name__=~%q,hostname=~%q}`,
		metricExpression, instanceExpression,
		metricExpression, instanceExpression,
		metricExpression, hostExpression,
		metricExpression, hostExpression,
		metricExpression, hostExpression,
		metricExpression, hostExpression)
}

func readManualFeedbackFeatureManifest(path string) (manualFeedbackFeatureManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return manualFeedbackFeatureManifest{}, fmt.Errorf("open manual feedback feature manifest: %w", err)
	}
	defer file.Close()
	var manifest manualFeedbackFeatureManifest
	if err := json.NewDecoder(file).Decode(&manifest); err != nil {
		return manualFeedbackFeatureManifest{}, fmt.Errorf("decode manual feedback feature manifest: %w", err)
	}
	return manifest, nil
}

func (s *Service) currentHumanFeedbackManifestSHA() (string, error) {
	type sourceRecord struct {
		Source              string    `json:"source"`
		ID                  uint      `json:"id"`
		RequestKey          string    `json:"request_key,omitempty"`
		GPUUUID             string    `json:"gpu_uuid,omitempty"`
		NodeIP              string    `json:"node_ip,omitempty"`
		FaultType           string    `json:"fault_type,omitempty"`
		OccurredAt          time.Time `json:"occurred_at,omitempty"`
		Status              string    `json:"status,omitempty"`
		TrainingEligible    bool      `json:"training_eligible,omitempty"`
		HistoryPackStatus   string    `json:"history_pack_status,omitempty"`
		WarningReviewStatus string    `json:"warning_review_status,omitempty"`
	}
	var labels []api.FailureLabel
	if err := s.db.Where("quality_tier = ? OR confirmation_resolution_id > ? OR confirmed_at IS NOT NULL", "confirmed", 0).
		Order("available_at ASC, id ASC").Find(&labels).Error; err != nil {
		return "", err
	}
	var outcomes []api.PredictionOutcomeEvaluation
	if err := s.db.Where("human_actual_value IS NOT NULL").Order("human_decided_at ASC, id ASC").Find(&outcomes).Error; err != nil {
		return "", err
	}
	var feedback []api.HardwareFaultFeedbackRequest
	if err := s.db.Order("fault_occurred_at ASC, id ASC").Find(&feedback).Error; err != nil {
		return "", err
	}
	records := make([]sourceRecord, 0, len(labels)+len(outcomes)+len(feedback))
	for _, row := range labels {
		records = append(records, sourceRecord{Source: "failure_label", ID: row.ID, GPUUUID: row.GPUUUID, NodeIP: row.NodeIP, FaultType: row.EventType, OccurredAt: row.OccurredAt, Status: row.QualityTier})
	}
	for _, row := range outcomes {
		records = append(records, sourceRecord{Source: "prediction_outcome", ID: row.ID, GPUUUID: row.GPUUUID, NodeIP: row.NodeIP, FaultType: row.ScopeEventType, OccurredAt: row.PredictionEvaluatedAt, Status: row.HumanOutcome})
	}
	for _, row := range feedback {
		records = append(records, sourceRecord{
			Source: "hardware_fault_feedback", ID: row.ID, RequestKey: row.RequestKey, GPUUUID: row.GPUUUID, NodeIP: row.NodeIP,
			FaultType: row.FaultType, OccurredAt: row.FaultOccurredAt, Status: row.Status, TrainingEligible: row.TrainingEligible,
			HistoryPackStatus: row.HistoryPackStatus, WarningReviewStatus: row.WarningReviewStatus,
		})
	}
	payload, err := json.Marshal(struct {
		Version string         `json:"version"`
		Records []sourceRecord `json:"records"`
	}{Version: manualFeedbackSourceManifestVersion, Records: records})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func manualFeedbackFeatureBlockers(row api.HardwareFaultFeedbackRequest) []string {
	reasons := []string{}
	if !row.TrainingEligible {
		return reasons
	}
	if manualFeedbackTargetScope(row) == "gpu" && (strings.TrimSpace(row.GPUUUID) == "" || strings.HasPrefix(row.IdentityResolutionStatus, "blocked") || row.IdentityResolutionStatus == "requires_historical_identity_at_fault_time") {
		reasons = append(reasons, fmt.Sprintf("feedback %d missing fault-time GPU identity", row.ID))
	}
	if row.HistoryPackStatus != "manifest_ready_pending_metric_extraction" || strings.TrimSpace(row.HistoryPackSHA256) == "" {
		reasons = append(reasons, fmt.Sprintf("feedback %d pre/post history pack is not manifest-ready", row.ID))
	}
	if !isManualFeedbackWarningReviewed(row.WarningReviewStatus) {
		reasons = append(reasons, fmt.Sprintf("feedback %d shadow warning coverage review is missing", row.ID))
	}
	return reasons
}

func manualFeedbackTargetScope(row api.HardwareFaultFeedbackRequest) string {
	if strings.TrimSpace(row.TargetScope) == "" {
		return "gpu"
	}
	return row.TargetScope
}

func manualFeedbackTimePrecision(row api.HardwareFaultFeedbackRequest) string {
	if strings.TrimSpace(row.FaultTimePrecision) == "" {
		return "exact"
	}
	return row.FaultTimePrecision
}

func manualFeedbackFaultWindow(row api.HardwareFaultFeedbackRequest) (time.Time, time.Time) {
	start := row.FaultOccurredAt
	end := row.FaultOccurredAt
	if row.FaultWindowStartAt != nil {
		start = *row.FaultWindowStartAt
	}
	if row.FaultWindowEndAt != nil {
		end = *row.FaultWindowEndAt
	}
	if end.Before(start) {
		return start, start
	}
	return start, end
}

func isManualFeedbackWarningReviewed(status string) bool {
	switch status {
	case "manual_feedback_no_prior_shadow_warning", "manual_feedback_prior_shadow_candidate_found", "manual_feedback_prior_shadow_below_threshold":
		return true
	default:
		return false
	}
}

func sourceKeyFromHistoryPackScope(scope string) string {
	for _, part := range strings.Fields(scope) {
		if strings.HasPrefix(part, "source_key=") {
			return strings.TrimSpace(strings.TrimPrefix(part, "source_key="))
		}
	}
	return ""
}

func writeManualFeedbackFeatureManifest(path string, manifest manualFeedbackFeatureManifest) (string, error) {
	if err := writeJSONAtomic(path, manifest); err != nil {
		return "", err
	}
	file, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(file)
	return hex.EncodeToString(sum[:]), nil
}
