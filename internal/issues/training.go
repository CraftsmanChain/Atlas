package issues

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"atlas/pkg/api"
	"atlas/pkg/storage"
	"gorm.io/gorm"
)

const (
	trainingDatasetSchema          = "atlas-issue-training-v1"
	trainingDatasetContractVersion = "1.1.0"
	trainingDatasetRedactionPolicy = "direct-identifiers-v1"
	trainingDatasetSplitPolicy     = "entity-hash-80-20-v1"
)

var (
	trainingIPv4Pattern    = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	trainingGPUUUIDPattern = regexp.MustCompile(`(?i)\bGPU-[0-9a-f-]{8,}\b`)
	trainingEmailPattern   = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
)

type trainingDataset struct {
	SchemaVersion   string                 `json:"schema_version"`
	ContractVersion string                 `json:"contract_version"`
	DatasetID       string                 `json:"dataset_id"`
	GeneratedAt     string                 `json:"generated_at"`
	RedactionPolicy string                 `json:"redaction_policy"`
	SplitPolicy     string                 `json:"split_policy"`
	QualityGates    trainingQualityGates   `json:"quality_gates"`
	Summary         trainingDatasetSummary `json:"summary"`
	Data            []trainingExample      `json:"data"`
}

type trainingQualityGates struct {
	HumanReviewed            bool `json:"human_reviewed"`
	ResolvedOnly             bool `json:"resolved_only"`
	CompleteResolutionFields bool `json:"complete_resolution_fields"`
	DirectIdentifiersRemoved bool `json:"direct_identifiers_removed"`
	LatestResolutionOnly     bool `json:"latest_resolution_only"`
}

type trainingDatasetSummary struct {
	Total      int            `json:"total"`
	Train      int            `json:"train"`
	Evaluation int            `json:"evaluation"`
	ByCategory map[string]int `json:"by_category"`
}

type trainingExample struct {
	SampleID                string         `json:"sample_id"`
	Split                   string         `json:"split"`
	Category                string         `json:"category"`
	IssueType               string         `json:"issue_type"`
	EntityType              string         `json:"entity_type"`
	Severity                string         `json:"severity"`
	DetectionSource         string         `json:"detection_source"`
	Title                   string         `json:"title"`
	Description             string         `json:"description"`
	RootCause               string         `json:"root_cause"`
	Solution                string         `json:"solution"`
	ResolutionProcess       string         `json:"resolution_process"`
	Result                  string         `json:"result"`
	Evidence                api.StringList `json:"evidence"`
	DetectionDurationSecond int64          `json:"detection_duration_seconds"`
	ResolutionDelaySecond   int64          `json:"resolution_delay_seconds"`
}

func buildTrainingDataset(db *storage.DB, generatedAt time.Time) (trainingDataset, error) {
	var resolutions []api.IssueResolution
	if err := db.Where("training_eligible = ? AND status = ?", true, "resolved").Order("id ASC").Find(&resolutions).Error; err != nil {
		return trainingDataset{}, err
	}

	rows := make([]trainingExample, 0, len(resolutions))
	summary := trainingDatasetSummary{ByCategory: map[string]int{}}
	for _, resolution := range resolutions {
		if !completeTrainingResolution(resolution) {
			continue
		}
		var issue api.PlatformIssue
		if err := db.First(&issue, resolution.IssueID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return trainingDataset{}, err
		}
		if issue.IssueType == deprecatedSourceDifferenceIssue || issue.Status != "resolved" || issue.LatestResolutionID != resolution.ID {
			continue
		}
		row := trainingExample{
			SampleID:                trainingSampleID(issue, resolution),
			Split:                   trainingSplit(issue),
			Category:                issue.Category,
			IssueType:               issue.IssueType,
			EntityType:              issue.EntityType,
			Severity:                issue.Severity,
			DetectionSource:         issue.DetectionSource,
			Title:                   redactTrainingText(issue.Title, issue),
			Description:             redactTrainingText(issue.Description, issue),
			RootCause:               redactTrainingText(resolution.RootCause, issue),
			Solution:                redactTrainingText(resolution.Solution, issue),
			ResolutionProcess:       redactTrainingText(resolution.ResolutionProcess, issue),
			Result:                  redactTrainingText(resolution.Result, issue),
			Evidence:                redactTrainingEvidence(resolution.Evidence, issue),
			DetectionDurationSecond: nonNegativeSeconds(issue.LastDetectedAt.Sub(issue.FirstDetectedAt)),
			ResolutionDelaySecond:   nonNegativeSeconds(resolution.CreatedAt.Sub(issue.LastDetectedAt)),
		}
		rows = append(rows, row)
		summary.Total++
		summary.ByCategory[row.Category]++
		if row.Split == "train" {
			summary.Train++
		} else {
			summary.Evaluation++
		}
	}

	datasetID, err := trainingDatasetID(rows)
	if err != nil {
		return trainingDataset{}, err
	}
	return trainingDataset{
		SchemaVersion:   trainingDatasetSchema,
		ContractVersion: trainingDatasetContractVersion,
		DatasetID:       datasetID,
		GeneratedAt:     generatedAt.UTC().Format(time.RFC3339),
		RedactionPolicy: trainingDatasetRedactionPolicy,
		SplitPolicy:     trainingDatasetSplitPolicy,
		QualityGates: trainingQualityGates{
			HumanReviewed:            true,
			ResolvedOnly:             true,
			CompleteResolutionFields: true,
			DirectIdentifiersRemoved: true,
			LatestResolutionOnly:     true,
		},
		Summary: summary,
		Data:    rows,
	}, nil
}

func countTrainingExamples(db *storage.DB) (int64, error) {
	var total int64
	err := db.Model(&api.IssueResolution{}).
		Joins("JOIN platform_issues ON platform_issues.id = issue_resolutions.issue_id").
		Where("issue_resolutions.training_eligible = ? AND issue_resolutions.status = ?", true, "resolved").
		Where("TRIM(issue_resolutions.root_cause) <> '' AND TRIM(issue_resolutions.solution) <> '' AND TRIM(issue_resolutions.resolution_process) <> '' AND TRIM(issue_resolutions.result) <> ''").
		Where("platform_issues.status = ? AND platform_issues.latest_resolution_id = issue_resolutions.id AND platform_issues.issue_type <> ?", "resolved", deprecatedSourceDifferenceIssue).
		Count(&total).Error
	return total, err
}

func completeTrainingResolution(resolution api.IssueResolution) bool {
	return resolution.TrainingEligible &&
		resolution.Status == "resolved" &&
		strings.TrimSpace(resolution.RootCause) != "" &&
		strings.TrimSpace(resolution.Solution) != "" &&
		strings.TrimSpace(resolution.ResolutionProcess) != "" &&
		strings.TrimSpace(resolution.Result) != ""
}

func trainingSplit(issue api.PlatformIssue) string {
	key := strings.Join([]string{issue.EntityType, issue.EntityKey, issue.NodeIP, issue.GPUUUID}, "|")
	if strings.Trim(key, "|") == "" {
		key = fmt.Sprintf("issue:%d", issue.ID)
	}
	sum := sha256.Sum256([]byte(key))
	if int(sum[0])%10 < 8 {
		return "train"
	}
	return "evaluation"
}

func redactTrainingText(value string, issue api.PlatformIssue) string {
	result := value
	for _, item := range []struct {
		value       string
		replacement string
	}{
		{issue.GPUUUID, "[GPU_UUID]"},
		{issue.NodeIP, "[NODE_IP]"},
		{issue.EntityKey, "[ENTITY_KEY]"},
	} {
		if len(strings.TrimSpace(item.value)) >= 7 {
			result = strings.ReplaceAll(result, item.value, item.replacement)
		}
	}
	result = trainingGPUUUIDPattern.ReplaceAllString(result, "[GPU_UUID]")
	result = trainingIPv4Pattern.ReplaceAllString(result, "[NODE_IP]")
	result = trainingEmailPattern.ReplaceAllString(result, "[EMAIL]")
	return strings.TrimSpace(result)
}

func redactTrainingEvidence(values api.StringList, issue api.PlatformIssue) api.StringList {
	if len(values) == 0 {
		return api.StringList{}
	}
	result := make(api.StringList, 0, len(values))
	for _, value := range values {
		result = append(result, redactTrainingText(value, issue))
	}
	return result
}

func trainingDatasetID(rows []trainingExample) (string, error) {
	payload, err := json.Marshal(struct {
		ContractVersion string            `json:"contract_version"`
		RedactionPolicy string            `json:"redaction_policy"`
		Data            []trainingExample `json:"data"`
	}{trainingDatasetContractVersion, trainingDatasetRedactionPolicy, rows})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "dataset-" + hex.EncodeToString(sum[:8]), nil
}

func trainingSampleID(issue api.PlatformIssue, resolution api.IssueResolution) string {
	value := fmt.Sprintf("%d:%d:%d", issue.ID, resolution.ID, resolution.CreatedAt.UnixNano())
	sum := sha256.Sum256([]byte(value))
	return "sample-" + hex.EncodeToString(sum[:8])
}

func nonNegativeSeconds(duration time.Duration) int64 {
	if duration < 0 {
		return 0
	}
	return int64(duration.Seconds())
}
