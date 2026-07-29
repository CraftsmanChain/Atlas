package evidence

import (
	"time"

	"atlas/pkg/api"
)

const (
	EvidenceSchemaVersion = "atlas-fault-evidence-v1"
	ReportSchemaVersion   = "atlas-fault-report-v1"
	ReportVersion         = "deterministic-v0.1.0"
)

type Text struct {
	ZH string `json:"zh"`
	EN string `json:"en"`
}

type Entity struct {
	Type       string `json:"type"`
	GPUAssetID uint   `json:"gpu_asset_id"`
	GPUUUID    string `json:"gpu_uuid"`
	NodeIP     string `json:"node_ip"`
	GPUIndex   int    `json:"gpu_index"`
	ModelName  string `json:"model_name"`
}

type Item struct {
	ID         string         `json:"evidence_id"`
	Kind       string         `json:"kind"`
	Source     string         `json:"source"`
	ObservedAt time.Time      `json:"observed_at"`
	Summary    Text           `json:"summary"`
	Detail     map[string]any `json:"detail"`
	Provenance string         `json:"provenance"`
}

type TimelineEntry struct {
	At         time.Time `json:"at"`
	EvidenceID string    `json:"evidence_id"`
	Label      Text      `json:"label"`
}

type SourceStatus struct {
	Source     string     `json:"source"`
	Status     string     `json:"status"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	Detail     Text       `json:"detail"`
}

type Gap struct {
	Code   string `json:"code"`
	Detail Text   `json:"detail"`
}

type Bundle struct {
	SchemaVersion   string                   `json:"schema_version"`
	GeneratedAt     time.Time                `json:"generated_at"`
	FaultEvent      api.GPUFaultEvent        `json:"fault_event"`
	Entity          Entity                   `json:"entity"`
	HealthScore     *api.GPUHealthScore      `json:"health_snapshot,omitempty"`
	FeatureSnapshot *api.GPUFeatureSnapshot  `json:"feature_snapshot,omitempty"`
	RuleHits        []api.GPUHealthRuleHit   `json:"rule_hits"`
	Issue           *api.PlatformIssue       `json:"issue,omitempty"`
	Resolutions     []api.IssueResolution    `json:"resolutions"`
	NodeEvidence    []api.NodeEvidenceRecord `json:"node_evidence"`
	Evidence        []Item                   `json:"evidence"`
	Timeline        []TimelineEntry          `json:"timeline"`
	SourceStatus    []SourceStatus           `json:"source_status"`
	MissingEvidence []Gap                    `json:"missing_evidence"`
	Limitations     []Text                   `json:"limitations"`
}

type Finding struct {
	Code        string   `json:"code"`
	Severity    string   `json:"severity"`
	Summary     Text     `json:"summary"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Hypothesis struct {
	Code        string   `json:"code"`
	Status      string   `json:"status"`
	Title       Text     `json:"title"`
	Reason      Text     `json:"reason"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Report struct {
	SchemaVersion             string          `json:"schema_version"`
	ReportVersion             string          `json:"report_version"`
	GeneratedAt               time.Time       `json:"generated_at"`
	AnalysisMode              string          `json:"analysis_mode"`
	EventID                   uint            `json:"event_id"`
	Title                     Text            `json:"title"`
	Summary                   Text            `json:"summary"`
	Severity                  string          `json:"severity"`
	AffectedEntity            Entity          `json:"affected_entity"`
	Timeline                  []TimelineEntry `json:"timeline"`
	Findings                  []Finding       `json:"findings"`
	Hypotheses                []Hypothesis    `json:"hypotheses"`
	MissingEvidence           []Gap           `json:"missing_evidence"`
	RecommendedReadonlyChecks []Text          `json:"recommended_readonly_checks"`
	OperatorActions           []Text          `json:"operator_actions"`
	Limitations               []Text          `json:"limitations"`
	NoActionExecuted          bool            `json:"no_action_executed"`
}
