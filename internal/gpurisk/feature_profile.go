package gpurisk

const AvailableContextProfile = "atlas_available_context_v1"
const AvailableDatasetVersion = "gpu-host-history-dataset-v3"
const AvailableFeatureContract = "gpu-host-history-available-context-v1"
const AvailableArtifactVersion = "gpu-host-mlp-artifact-v2"
const AvailableRankerProfile = "hearank-atlas-available-context-v1"

// AvailabilityManifest is fixed by the binary, never a caller-defined ignore list.
type AvailabilityManifest struct {
	Version          string   `json:"version"`
	SchemaSHA        string   `json:"schema_sha"`
	OptionalBlockers []string `json:"optional_blockers"`
	Missing          string   `json:"missing"`
	SHA              string   `json:"sha"`
}

func availabilityManifest() *AvailabilityManifest {
	schema, _ := digest(CoreSchema())
	m := &AvailabilityManifest{Version: "gpu-context-availability-policy-v1", SchemaSHA: schema, OptionalBlockers: []string{}, Missing: "null_missing_channel_separate_from_oov_train_only_vocabulary"}
	for _, n := range []string{"host_type", "gpu_model", "manufacturer", "main_board", "quota_group", "data_center", "rack"} {
		m.OptionalBlockers = append(m.OptionalBlockers, "host_context_unknown:"+n)
	}
	m.SHA, _ = digest(m)
	return m
}
func validAvailability(m *AvailabilityManifest) bool {
	if m == nil {
		return false
	}
	a, _ := digest(m)
	b, _ := digest(availabilityManifest())
	return a == b
}
func optionalContextBlocker(b string) bool {
	for _, v := range availabilityManifest().OptionalBlockers {
		if b == v {
			return true
		}
	}
	return false
}
func profileBlocks(bs []string) bool {
	for _, b := range bs {
		if !optionalContextBlocker(b) {
			return true
		}
	}
	return false
}
func datasetTuple(d Dataset) bool {
	if d.Spec.Profile == "paper_core" {
		return d.Version == DatasetVersion && d.FeatureContract == HistoryFeatureVersion && d.Availability == nil && d.ProfileFeatureComplete == nil
	}
	return d.Spec.Profile == AvailableContextProfile && d.Version == AvailableDatasetVersion && d.FeatureContract == AvailableFeatureContract && validAvailability(d.Availability) && d.ProfileFeatureComplete != nil
}
func artifactTuple(a RankerArtifact) bool {
	if a.Version == RankerVersion {
		return a.Profile == RankerProfile && a.FeatureContract == HistoryFeatureVersion && a.Availability == nil
	}
	return a.Version == AvailableArtifactVersion && a.Profile == AvailableRankerProfile && a.FeatureContract == AvailableFeatureContract && validAvailability(a.Availability)
}
func datasetFeatureReady(d Dataset) bool {
	if !datasetTuple(d) {
		return false
	}
	if d.Spec.Profile == "paper_core" {
		return d.PaperCoreComplete
	}
	return *d.ProfileFeatureComplete
}
func modelDatasetTuple(a RankerArtifact, d Dataset) bool {
	return artifactTuple(a) && datasetTuple(d) && a.FeatureContract == d.FeatureContract && (a.Version == RankerVersion) == (d.Spec.Profile == "paper_core")
}
