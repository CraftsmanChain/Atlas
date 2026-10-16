package diskhealth

import (
	"encoding/json"
	"time"
)

const (
	SchemaVersion     = "hardware-health-report-v1"
	RuleVersion       = "disk-health-v1.3.0"
	ReportRuleVersion = "hardware-health-v1.4.0"
	MemoryRuleVersion = "memory-health-v0.1.0"
	MaxInputBytes     = 256 * 1024
	MaxBatchBytes     = 2 * 1024 * 1024
	MaxInputs         = 200
)

type InputManifest struct {
	Schema string  `json:"schema"`
	Inputs []Input `json:"inputs"`
}

type Input struct {
	Adapter        string    `json:"adapter,omitempty"`
	Reference      string    `json:"reference"`
	File           string    `json:"file"`
	SHA256         string    `json:"sha256"`
	Scope          string    `json:"scope"`
	Node           string    `json:"node"`
	Generation     string    `json:"generation"`
	BootID         string    `json:"boot_id"`
	Source         string    `json:"source"`
	SourceKind     string    `json:"source_kind"`
	ObservedAt     time.Time `json:"observed_at"`
	AvailableAt    time.Time `json:"available_at"`
	MaxAgeSeconds  int       `json:"max_age_seconds"`
	ExitCode       *int      `json:"exit_code"`
	Truncated      bool      `json:"truncated"`
	VolatileBackup *bool     `json:"volatile_backup_supported,omitempty"`
	PMR            *bool     `json:"pmr_supported,omitempty"`
	Baseline       *Input    `json:"baseline,omitempty"`
}

type Evidence struct {
	ID    string `json:"id"`
	Field string `json:"field"`
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

type Hit struct {
	Rule       string `json:"rule"`
	Level      string `json:"level"`
	EvidenceID string `json:"evidence_id"`
}

type Result struct {
	RuleVersion      string         `json:"rule_version,omitempty"`
	Memory           *MemoryDetails `json:"memory,omitempty"`
	Disk             *DiskDetails   `json:"disk,omitempty"`
	Reference        string         `json:"reference"`
	EntityID         string         `json:"entity_id"`
	EntityType       string         `json:"entity_type"`
	IdentityStatus   string         `json:"identity_status"`
	Scope            string         `json:"scope"`
	Node             string         `json:"node"`
	Generation       string         `json:"generation"`
	BootID           string         `json:"boot_id"`
	Serial           string         `json:"serial"`
	Model            string         `json:"model"`
	Firmware         string         `json:"firmware"`
	Protocol         string         `json:"protocol"`
	Device           string         `json:"device"`
	ToolVersion      string         `json:"tool_version"`
	Source           string         `json:"source"`
	SourceKind       string         `json:"source_kind"`
	InputSHA256      string         `json:"input_sha256"`
	ObservedAt       time.Time      `json:"observed_at"`
	AvailableAt      time.Time      `json:"available_at"`
	Freshness        string         `json:"freshness"`
	Support          string         `json:"support"`
	AssessmentStatus string         `json:"assessment_status"`
	Level            string         `json:"level"`
	ExitCode         *int           `json:"exit_code"`
	MediaDelta       *string        `json:"media_delta"`
	BaselineSHA256   string         `json:"baseline_sha256"`
	TrendStatus      string         `json:"trend_status"`
	Blockers         []string       `json:"blockers"`
	Hits             []Hit          `json:"hits"`
	Evidence         []Evidence     `json:"evidence"`
}

type DiskDetails struct {
	MediaType   string          `json:"media_type"`
	Profile     string          `json:"profile"`
	Temperature *string         `json:"temperature_celsius"`
	Wear        *WearIndicator  `json:"wear"`
	Dimensions  []DiskDimension `json:"dimensions"`
	Deltas      []CounterDelta  `json:"counter_deltas"`
}

type WearIndicator struct {
	Field      string `json:"field"`
	Value      string `json:"value"`
	Unit       string `json:"unit"`
	Kind       string `json:"kind"`
	EvidenceID string `json:"evidence_id"`
}

type DiskDimension struct {
	ID    string `json:"id"`
	Level string `json:"level"`
	State string `json:"state"`
}

type CounterDelta struct {
	Field      string `json:"field"`
	Value      string `json:"value"`
	Unit       string `json:"unit"`
	EvidenceID string `json:"evidence_id"`
}

type MemoryDetails struct {
	Controller        string  `json:"controller"`
	Driver            string  `json:"driver"`
	KernelRelease     string  `json:"kernel_release"`
	LocationPrecision string  `json:"location_precision"`
	CEDelta           *string `json:"ce_delta"`
	UEDelta           *string `json:"ue_delta"`
}

type Summary struct {
	Total    int            `json:"total"`
	Assessed int            `json:"assessed"`
	Unknown  int            `json:"unknown"`
	ByLevel  map[string]int `json:"by_level"`
}

type Report struct {
	Schema              string    `json:"schema"`
	InputManifestSHA256 string    `json:"input_manifest_sha256"`
	RuleVersion         string    `json:"rule_version"`
	Mode                string    `json:"mode"`
	AsOf                time.Time `json:"as_of"`
	AlertsEmitted       bool      `json:"alerts_emitted"`
	ActionsExecuted     bool      `json:"actions_executed"`
	Summary             Summary   `json:"summary"`
	Rows                []Result  `json:"rows"`
}

func newResult(in Input, raw []byte) Result {
	r := Result{Reference: in.Reference, EntityID: Digest([]byte(in.Reference + ":" + Digest(raw))), EntityType: "disk", IdentityStatus: "provisional",
		Scope: in.Scope, Node: in.Node, Generation: in.Generation, BootID: in.BootID,
		Source: in.Source, SourceKind: in.SourceKind, InputSHA256: Digest(raw),
		ObservedAt: in.ObservedAt, AvailableAt: in.AvailableAt, ExitCode: in.ExitCode,
		Freshness: "unknown", Support: "unsupported", AssessmentStatus: "insufficient_data",
		Level: "unknown", TrendStatus: "unavailable", Blockers: []string{}, Hits: []Hit{}, Evidence: []Evidence{}}
	if in.SourceKind != "fixture" && in.SourceKind != "operator_snapshot" {
		r.SourceKind = ""
		r.block("source_kind_unknown")
	}
	if in.ExitCode != nil && (*in.ExitCode < 0 || *in.ExitCode > 255) {
		r.ExitCode = nil
		r.block("execution_exit_out_of_range")
	}
	return r
}

func (r *Result) block(reason string) {
	for _, value := range r.Blockers {
		if value == reason {
			return
		}
	}
	r.Blockers = append(r.Blockers, reason)
}

func (r *Result) evidence(field, value, unit string) string {
	version := r.RuleVersion
	if version == "" {
		version = RuleVersion
	}
	identity, _ := json.Marshal([]string{r.InputSHA256, r.BaselineSHA256, version, field, value, unit})
	id := Digest(identity)
	r.Evidence = append(r.Evidence, Evidence{ID: id, Field: field, Value: value, Unit: unit})
	return id
}

func (r *Result) hit(rule, level, evidenceID string) {
	for _, hit := range r.Hits {
		if hit.Rule == rule {
			return
		}
	}
	r.Hits = append(r.Hits, Hit{Rule: rule, Level: level, EvidenceID: evidenceID})
}

func (r *Result) finish() {
	r.Level = "clear_for_checked_signals"
	r.AssessmentStatus = "assessed"
	if len(r.Blockers) > 0 {
		r.Level = "unknown"
		r.AssessmentStatus = "insufficient_data"
	}
	for _, h := range r.Hits {
		if Severity(h.Level) > Severity(r.Level) {
			r.Level = h.Level
		}
	}
	r.finishDiskDimensions()
}

func Severity(level string) int {
	switch level {
	case "critical":
		return 3
	case "warning":
		return 2
	case "attention":
		return 1
	default:
		return 0
	}
}
