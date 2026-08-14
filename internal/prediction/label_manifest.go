package prediction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
)

const LabelManifestVersion = "prediction-label-manifest-v1"

type LabelManifest struct {
	Version             string         `json:"version"`
	LabelContract       string         `json:"label_contract_version"`
	Mode                string         `json:"mode"`
	Total               int            `json:"total"`
	Positive            int            `json:"positive"`
	Excluded            int            `json:"excluded"`
	Confirmed           int            `json:"confirmed"`
	StrongProxy         int            `json:"strong_proxy"`
	WeakProxy           int            `json:"weak_proxy"`
	HumanConfirmed      int            `json:"human_confirmed"`
	QualityGateStatus   string         `json:"quality_gate_status"`
	BlockingReasons     []string       `json:"blocking_reasons"`
	RecommendedNextRun  []string       `json:"recommended_next_run"`
	ManifestSHA256      string         `json:"manifest_sha256"`
	SampleLabelKeys     []string       `json:"sample_label_keys"`
	ByEventType         map[string]int `json:"by_event_type"`
	ByModel             map[string]int `json:"by_model"`
	BySourceType        map[string]int `json:"by_source_type"`
	ByRuleVersion       map[string]int `json:"by_rule_version"`
	ExclusionReasons    map[string]int `json:"exclusion_reasons"`
	PositivePolicy      []string       `json:"positive_policy"`
	NegativePolicy      string         `json:"negative_policy"`
	GraySamplePolicy    []string       `json:"gray_sample_policy"`
	HumanEvidencePolicy []string       `json:"human_evidence_policy"`
	EvidenceScope       []string       `json:"evidence_scope"`
	KnownGaps           []string       `json:"known_gaps"`
	GeneratedAt         time.Time      `json:"generated_at"`
}

func (s *Service) LabelManifest() (LabelManifest, error) {
	var labels []api.FailureLabel
	if err := s.db.Order("available_at ASC, id ASC").Find(&labels).Error; err != nil {
		return LabelManifest{}, err
	}
	manifest := LabelManifest{
		Version: LabelManifestVersion, LabelContract: LabelContractVersion, Mode: "read_only_label_governance",
		ByEventType: map[string]int{}, ByModel: map[string]int{}, BySourceType: map[string]int{},
		ByRuleVersion: map[string]int{}, ExclusionReasons: map[string]int{}, GeneratedAt: s.now(),
		PositivePolicy: []string{
			"confirmed operator or repair evidence is the highest-quality positive label",
			"strong proxy labels may train shadow candidates but must remain distinguishable from confirmed failures",
			"weak proxy labels are reviewable evidence and should not be silently promoted to confirmed labels",
		},
		NegativePolicy: "negative samples are valid only after the prediction horizon and censoring window pass without an eligible positive label",
		GraySamplePolicy: []string{
			"excluded labels are never counted as positives",
			"labels with missing stable identity remain review material until point-in-time identity evidence is available",
			"operational-only events require corroborating hardware, repair, replacement, or operator evidence before training use",
		},
		HumanEvidencePolicy: []string{
			"human overrides must include operator identity, reason, and repair/replacement/diagnostic evidence",
			"manual decisions override final outcome only; rule-derived provenance remains immutable",
		},
		EvidenceScope: []string{
			"failure_labels table",
			"label contract and quality-tier policy",
			"exclusion reasons and human confirmation references",
		},
	}
	for _, label := range labels {
		manifest.Total++
		if len(manifest.SampleLabelKeys) < 20 {
			manifest.SampleLabelKeys = append(manifest.SampleLabelKeys, label.LabelKey)
		}
		if label.Excluded {
			manifest.Excluded++
			reason := strings.TrimSpace(label.ExclusionReason)
			if reason == "" {
				reason = "unspecified"
			}
			manifest.ExclusionReasons[reason]++
			continue
		}
		if label.LabelValue == 1 {
			manifest.Positive++
		}
		switch label.QualityTier {
		case "confirmed":
			manifest.Confirmed++
		case "strong_proxy":
			manifest.StrongProxy++
		case "weak_proxy":
			manifest.WeakProxy++
		}
		if label.ConfirmationResolutionID > 0 || label.ConfirmedAt != nil {
			manifest.HumanConfirmed++
		}
		increment(manifest.ByEventType, label.EventType)
		increment(manifest.ByModel, label.ModelName)
		increment(manifest.BySourceType, label.SourceType)
		increment(manifest.ByRuleVersion, label.RuleVersion)
	}
	manifest.KnownGaps = labelManifestGaps(manifest)
	manifest.QualityGateStatus, manifest.BlockingReasons, manifest.RecommendedNextRun = labelManifestQualityGate(manifest)
	manifest.ManifestSHA256 = labelManifestChecksum(manifest)
	return manifest, nil
}

func increment(counts map[string]int, key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "unknown"
	}
	counts[key]++
}

func labelManifestGaps(manifest LabelManifest) []string {
	gaps := []string{}
	if manifest.Total == 0 {
		gaps = append(gaps, "no failure labels have been materialized")
	}
	if manifest.Confirmed == 0 {
		gaps = append(gaps, "no confirmed repair/operator positive labels are available")
	}
	if manifest.Excluded > 0 {
		gaps = append(gaps, "excluded labels exist and must stay out of positive training/evaluation denominators")
	}
	if manifest.WeakProxy > manifest.Confirmed+manifest.StrongProxy {
		gaps = append(gaps, "weak proxy labels dominate higher-confidence evidence")
	}
	sort.Strings(gaps)
	return gaps
}

func labelManifestQualityGate(manifest LabelManifest) (string, []string, []string) {
	reasons := []string{}
	next := []string{}
	if manifest.Positive == 0 {
		reasons = append(reasons, "no eligible positive labels")
		next = append(next, "materialize rule-derived labels after point-in-time event windows mature")
	}
	if manifest.Confirmed == 0 {
		reasons = append(reasons, "no confirmed human/repair labels")
		next = append(next, "prioritize operator review for recent strong-proxy hardware episodes")
	}
	if manifest.StrongProxy == 0 && manifest.Confirmed == 0 {
		reasons = append(reasons, "no high-confidence labels available")
		next = append(next, "keep supervised model validation blocked until confirmed or strong-proxy labels exist")
	}
	if manifest.WeakProxy > manifest.Confirmed+manifest.StrongProxy {
		reasons = append(reasons, "weak proxy labels exceed confirmed plus strong-proxy labels")
		next = append(next, "separate weak-proxy evidence from training-positive denominators")
	}
	if manifest.Excluded > 0 {
		next = append(next, "audit excluded labels before rebuilding training cohorts")
	}
	if len(reasons) > 0 {
		sort.Strings(reasons)
		return "blocked", reasons, uniqueSorted(next)
	}
	next = append(next, "export the label manifest with its SHA256 before running offline model validation")
	if manifest.Confirmed < 3 {
		next = append(next, "treat metrics as exploratory until more confirmed labels accumulate")
		return "exploratory_ready", nil, uniqueSorted(next)
	}
	return "ready_for_offline_validation", nil, uniqueSorted(next)
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func labelManifestChecksum(manifest LabelManifest) string {
	fingerprint := struct {
		Version             string         `json:"version"`
		LabelContract       string         `json:"label_contract_version"`
		Mode                string         `json:"mode"`
		Total               int            `json:"total"`
		Positive            int            `json:"positive"`
		Excluded            int            `json:"excluded"`
		Confirmed           int            `json:"confirmed"`
		StrongProxy         int            `json:"strong_proxy"`
		WeakProxy           int            `json:"weak_proxy"`
		HumanConfirmed      int            `json:"human_confirmed"`
		QualityGateStatus   string         `json:"quality_gate_status"`
		BlockingReasons     []string       `json:"blocking_reasons"`
		RecommendedNextRun  []string       `json:"recommended_next_run"`
		SampleLabelKeys     []string       `json:"sample_label_keys"`
		ByEventType         map[string]int `json:"by_event_type"`
		ByModel             map[string]int `json:"by_model"`
		BySourceType        map[string]int `json:"by_source_type"`
		ByRuleVersion       map[string]int `json:"by_rule_version"`
		ExclusionReasons    map[string]int `json:"exclusion_reasons"`
		PositivePolicy      []string       `json:"positive_policy"`
		NegativePolicy      string         `json:"negative_policy"`
		GraySamplePolicy    []string       `json:"gray_sample_policy"`
		HumanEvidencePolicy []string       `json:"human_evidence_policy"`
		EvidenceScope       []string       `json:"evidence_scope"`
		KnownGaps           []string       `json:"known_gaps"`
	}{
		Version: manifest.Version, LabelContract: manifest.LabelContract, Mode: manifest.Mode,
		Total: manifest.Total, Positive: manifest.Positive, Excluded: manifest.Excluded,
		Confirmed: manifest.Confirmed, StrongProxy: manifest.StrongProxy, WeakProxy: manifest.WeakProxy,
		HumanConfirmed: manifest.HumanConfirmed, QualityGateStatus: manifest.QualityGateStatus,
		BlockingReasons: manifest.BlockingReasons, RecommendedNextRun: manifest.RecommendedNextRun,
		SampleLabelKeys: manifest.SampleLabelKeys,
		ByEventType:     manifest.ByEventType, ByModel: manifest.ByModel, BySourceType: manifest.BySourceType,
		ByRuleVersion: manifest.ByRuleVersion, ExclusionReasons: manifest.ExclusionReasons,
		PositivePolicy: manifest.PositivePolicy, NegativePolicy: manifest.NegativePolicy,
		GraySamplePolicy: manifest.GraySamplePolicy, HumanEvidencePolicy: manifest.HumanEvidencePolicy,
		EvidenceScope: manifest.EvidenceScope, KnownGaps: manifest.KnownGaps,
	}
	payload, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
