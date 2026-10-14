package api

import "time"

// PhysicalDiskSnapshot stores the center-side, exact result of an already
// approved hardware-health report row. It is a cache/history record only:
// reads of this table must never execute device commands.
type PhysicalDiskSnapshot struct {
	ID                  uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	SnapshotKey         string     `json:"snapshot_key" gorm:"uniqueIndex;size:64;not null"`
	Current             bool       `json:"current" gorm:"index;not null;default:false"`
	ReportSHA256        string     `json:"report_sha256" gorm:"index;size:64"`
	InputManifestSHA256 string     `json:"input_manifest_sha256" gorm:"size:64"`
	ReportRuleVersion   string     `json:"report_rule_version" gorm:"index"`
	RuleVersion         string     `json:"rule_version" gorm:"index"`
	Reference           string     `json:"reference" gorm:"index"`
	EntityID            string     `json:"entity_id" gorm:"index;size:64;not null"`
	EntityType          string     `json:"entity_type" gorm:"index"`
	IdentityStatus      string     `json:"identity_status" gorm:"index"`
	Scope               string     `json:"scope" gorm:"index"`
	Node                string     `json:"node" gorm:"index"`
	Generation          string     `json:"generation" gorm:"index"`
	BootID              string     `json:"boot_id" gorm:"index"`
	Serial              string     `json:"serial" gorm:"index"`
	Model               string     `json:"model" gorm:"index"`
	Firmware            string     `json:"firmware" gorm:"index"`
	Protocol            string     `json:"protocol" gorm:"index"`
	Device              string     `json:"device" gorm:"index"`
	MediaType           string     `json:"media_type" gorm:"index"`
	DiskProfile         string     `json:"disk_profile" gorm:"index"`
	ToolVersion         string     `json:"tool_version"`
	Source              string     `json:"source" gorm:"index"`
	SourceKind          string     `json:"source_kind" gorm:"index"`
	InputSHA256         string     `json:"input_sha256" gorm:"index;size:64"`
	BaselineSHA256      string     `json:"baseline_sha256" gorm:"size:64"`
	Freshness           string     `json:"freshness" gorm:"index"`
	Support             string     `json:"support" gorm:"index"`
	AssessmentStatus    string     `json:"assessment_status" gorm:"index"`
	Level               string     `json:"level" gorm:"index"`
	ExitCode            *int       `json:"exit_code"`
	MediaDelta          *string    `json:"media_delta,omitempty"`
	TrendStatus         string     `json:"trend_status" gorm:"index"`
	Blockers            StringList `json:"blockers" gorm:"type:text"`
	DiskJSON            string     `json:"-" gorm:"type:text"`
	MemoryJSON          string     `json:"-" gorm:"type:text"`
	HitsJSON            string     `json:"-" gorm:"type:text"`
	EvidenceJSON        string     `json:"-" gorm:"type:text"`
	ObservedAt          time.Time  `json:"observed_at" gorm:"index"`
	AvailableAt         time.Time  `json:"available_at" gorm:"index"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// DiskHealthEpisode is a deterministic review episode derived from disk rule
// hits. It is intentionally read-only/shadow-facing until notification policy
// is separately designed and approved.
type DiskHealthEpisode struct {
	ID               uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	EpisodeKey       string     `json:"episode_key" gorm:"uniqueIndex;size:64;not null"`
	EntityID         string     `json:"entity_id" gorm:"index;size:64;not null"`
	EntityType       string     `json:"entity_type" gorm:"index"`
	Node             string     `json:"node" gorm:"index"`
	Device           string     `json:"device" gorm:"index"`
	Serial           string     `json:"serial" gorm:"index"`
	Model            string     `json:"model" gorm:"index"`
	RuleCode         string     `json:"rule_code" gorm:"index"`
	Severity         string     `json:"severity" gorm:"index"`
	State            string     `json:"state" gorm:"index"`
	EvidenceID       string     `json:"evidence_id" gorm:"size:64"`
	OccurrenceCount  int        `json:"occurrence_count"`
	LatestSnapshotID uint       `json:"latest_snapshot_id" gorm:"index"`
	RuleVersion      string     `json:"rule_version" gorm:"index"`
	FirstObservedAt  time.Time  `json:"first_observed_at" gorm:"index"`
	LastObservedAt   time.Time  `json:"last_observed_at" gorm:"index"`
	RecoveredAt      *time.Time `json:"recovered_at,omitempty" gorm:"index"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}
