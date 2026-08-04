package prediction

import (
	"fmt"
	"sort"

	"atlas/internal/features"
	"atlas/internal/featurestats"
	"atlas/pkg/api"
	"gorm.io/gorm/clause"
)

func (s *Service) FeatureParityAudits(limit int) ([]api.PredictionFeatureParityAudit, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var rows []api.PredictionFeatureParityAudit
	err := s.db.Order("audited_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Service) SyncFeatureParityAudits() error {
	var specs []api.PredictionModelSpec
	if err := s.db.Where("current = ? AND status = ?", true, "shadow_candidate").Order("id").Find(&specs).Error; err != nil {
		return err
	}
	for _, spec := range specs {
		if err := s.auditFeatureParity(spec); err != nil {
			return fmt.Errorf("model spec %d: %w", spec.ID, err)
		}
	}
	return nil
}

func (s *Service) auditFeatureParity(spec api.PredictionModelSpec) error {
	checksum, err := sha256File(spec.ArtifactURI)
	if err != nil {
		return fmt.Errorf("verify registered artifact: %w", err)
	}
	if checksum != spec.ArtifactSHA256 {
		return fmt.Errorf("registered artifact checksum mismatch")
	}
	var artifact registryArtifact
	if err := decodeRegistryJSON(spec.ArtifactURI, &artifact); err != nil {
		return err
	}
	var columns []string
	for _, model := range artifact.Models {
		if model.HorizonMinutes == spec.HorizonMinutes {
			columns = model.FeatureColumns
			break
		}
	}
	if len(columns) == 0 {
		return fmt.Errorf("registered horizon has no feature columns")
	}
	activeSources := map[string]struct{}{}
	for _, definition := range features.Builtins() {
		if definition.Status == "active" && stringListContains(definition.Purposes, "prediction") {
			activeSources[definition.Name] = struct{}{}
		}
	}
	audit := api.PredictionFeatureParityAudit{
		ModelSpecID: spec.ID, ModelKey: spec.ModelKey, ModelVersion: spec.Version,
		SourceBaselineBuildID: spec.SourceBaselineBuildID, ArtifactSHA256: spec.ArtifactSHA256,
		FeatureContractVersion:        spec.FeatureContractVersion,
		TransformationContractVersion: featurestats.Trailing24hContractVersion,
		TrainingFeatureCount:          len(columns), ScoringAllowed: false, AuditedAt: s.now(),
		SourceMetrics: api.StringList{}, ContractMatchedColumns: api.StringList{},
		MissingSourceColumns: api.StringList{}, UnsupportedTransformColumns: api.StringList{},
		BlockingReasons: api.StringList{},
	}
	sources := map[string]struct{}{}
	for _, column := range columns {
		source, _, supported := featurestats.ParseTrailing24hColumn(column)
		if !supported {
			audit.UnsupportedTransformColumns = append(audit.UnsupportedTransformColumns, column)
			continue
		}
		if _, exists := activeSources[source]; !exists {
			audit.MissingSourceColumns = append(audit.MissingSourceColumns, column)
			continue
		}
		audit.ContractMatchedColumns = append(audit.ContractMatchedColumns, column)
		sources[source] = struct{}{}
	}
	for source := range sources {
		audit.SourceMetrics = append(audit.SourceMetrics, source)
	}
	sort.Strings(audit.SourceMetrics)
	sort.Strings(audit.ContractMatchedColumns)
	sort.Strings(audit.MissingSourceColumns)
	sort.Strings(audit.UnsupportedTransformColumns)
	audit.ContractMatchedCount = len(audit.ContractMatchedColumns)
	audit.SourceMetricCount = len(audit.SourceMetrics)
	audit.MissingSourceCount = len(audit.MissingSourceColumns)
	audit.UnsupportedTransformCount = len(audit.UnsupportedTransformColumns)
	switch {
	case spec.FeatureContractVersion != features.CatalogVersion:
		audit.Status = "blocked_contract_version"
		audit.BlockingReasons = append(audit.BlockingReasons, "feature_catalog_version_mismatch")
	case audit.ContractMatchedCount != audit.TrainingFeatureCount:
		audit.Status = "blocked_contract_gap"
		if audit.MissingSourceCount > 0 {
			audit.BlockingReasons = append(audit.BlockingReasons, "online_source_metric_missing")
		}
		if audit.UnsupportedTransformCount > 0 {
			audit.BlockingReasons = append(audit.BlockingReasons, "transformation_not_shared")
		}
	default:
		audit.Status = "replay_required"
		audit.BlockingReasons = append(audit.BlockingReasons, "historical_value_replay_not_verified", "live_24h_coverage_not_verified")
	}
	var existing api.PredictionFeatureParityAudit
	result := s.db.Where("model_spec_id = ?", spec.ID).Limit(1).Find(&existing)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 && audit.Status == "replay_required" && existing.ArtifactSHA256 == audit.ArtifactSHA256 &&
		(existing.Status == "live_coverage_required" || existing.Status == "blocked_replay") {
		audit.Status = existing.Status
		audit.ReplayVerifiedCount = existing.ReplayVerifiedCount
		audit.BlockingReasons = append(api.StringList(nil), existing.BlockingReasons...)
	}
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "model_spec_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"model_key", "model_version", "source_baseline_build_id", "artifact_sha256",
			"feature_contract_version", "transformation_contract_version", "status",
			"training_feature_count", "contract_matched_count", "source_metric_count",
			"missing_source_count", "unsupported_transform_count", "replay_verified_count",
			"source_metrics", "contract_matched_columns", "missing_source_columns",
			"unsupported_transform_columns", "blocking_reasons", "scoring_allowed",
			"audited_at", "updated_at",
		}),
	}).Create(&audit).Error
}

func stringListContains(values api.StringList, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
