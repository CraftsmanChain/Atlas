package gpurisk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
)

const DatasetVersion = "gpu-host-history-dataset-v2"
const HistoryFeatureVersion = "gpu-host-history-v2"

// MaxSourceLag bounds the unknown tail after the last complete source pull.
const MaxSourceLag = 24 * time.Hour

// DatasetSpec pins immutable outcome batches. There is deliberately no "latest"
// selector, automatic training, label balancing or request-time asset lookup.
type DatasetSpec struct {
	BatchIDs      []uint    `json:"batch_ids"`
	Anchor        time.Time `json:"anchor"`
	TrainEnd      time.Time `json:"train_end"`
	ValidationEnd time.Time `json:"validation_end"`
	TestEnd       time.Time `json:"test_end"`
	Profile       string    `json:"profile"`
}
type FeatureField struct {
	KnownAt     string `json:"known_at"`
	Producer    string `json:"producer"`
	Target      string `json:"target"`
	Sensitivity string `json:"sensitivity"`
	Name        string `json:"name"`
	Dtype       string `json:"dtype"`
	Unit        string `json:"unit"`
	Missing     string `json:"missing"`
	Group       string `json:"group"`
}

func featureField(name, dtype, unit, missing, group string) FeatureField {
	return FeatureField{Name: name, Dtype: dtype, Unit: unit, Missing: missing, Group: group, KnownAt: "onset and source availability strictly before cutoff; calendar at cutoff", Producer: HistoryFeatureVersion, Target: Target, Sensitivity: "private_hardware"}
}

func CoreSchema() []FeatureField {
	result := []FeatureField{}
	for _, v := range []struct{ name, unit, group string }{
		{"episode_count", "episodes", "composition"}, {"episode_count_30d", "episodes", "recurrence"}, {"mean_interval_days", "days", "recurrence"}, {"frequency_per_day", "episodes/day", "recurrence"}, {"recent_ratio", "ratio", "recurrence"}, {"unresolved_count", "episodes", "composition"},
	} {
		result = append(result, featureField(v.name, "nullable_float64", v.unit, "null, never zero-fill unknown", v.group))
	}
	for _, name := range []string{"host_id", "host_type", "gpu_model", "manufacturer", "main_board", "quota_group", "data_center", "rack"} {
		result = append(result, featureField(name, "nullable_category", "category", "null; vocabulary fit only on train", "host_context"))
	}
	for _, name := range []string{"weekday", "weekend"} {
		result = append(result, featureField(name, "float64", "Asia/Shanghai calendar", "not missing", "calendar"))
	}
	// Type/severity counts retain their raw categorical keys, no vocabulary is fit.
	for _, name := range []string{"by_fault_type", "by_severity"} {
		result = append(result, featureField(name, "nullable_count_map", "episodes", "null if source does not supply semantics", "composition"))
	}
	return result
}

type HistoryFeatures struct {
	HistoryUnknown      bool                `json:"history_unknown"`
	Numeric             map[string]*float64 `json:"numeric"`
	Categorical         map[string]*string  `json:"categorical"`
	ByFaultType         map[string]int      `json:"by_fault_type"`
	BySeverity          map[string]int      `json:"by_severity"`
	NoHistory           *bool               `json:"no_history"`
	HistoryCoverageDays *float64            `json:"history_coverage_days"`
	EvidenceSHAs        []string            `json:"evidence_shas"`
	Blockers            []string            `json:"blockers"`
}

// Feature maps are closed to the core columns. Count-map keys are categorical
// values, not feature columns; unknown/null core values remain permitted.
func validateCoreFeatures(f HistoryFeatures) error {
	numeric, categorical := map[string]bool{}, map[string]bool{}
	for _, field := range CoreSchema() {
		switch field.Dtype {
		case "float64", "nullable_float64":
			numeric[field.Name] = true
		case "nullable_category":
			categorical[field.Name] = true
		}
	}
	for name := range f.Numeric {
		if !numeric[name] {
			return fmt.Errorf("%w: non-core numeric feature %q", ErrInput, name)
		}
	}
	for name := range f.Categorical {
		if !categorical[name] {
			return fmt.Errorf("%w: non-core categorical feature %q", ErrInput, name)
		}
	}
	return nil
}

// Validate before constructing the rows consumed by either feature fingerprint.
// A producer adding a column must not silently create a new core artifact.
func coreFeatureRows(d Dataset) ([]any, error) {
	core := CoreSchema()
	if len(d.Schema) != len(core) {
		return nil, ErrInput
	}
	for i, field := range core {
		if d.Schema[i] != field {
			return nil, ErrInput
		}
	}
	rows := []any{}
	for _, r := range d.Rows {
		if err := validateCoreFeatures(r.Features); err != nil {
			return nil, err
		}
		rows = append(rows, []any{r.QueryID, r.NodeID, r.Generation, r.Cutoff, r.Features})
	}
	return rows, nil
}

type DatasetRow struct {
	QueryID     uint            `json:"query_id"`
	BatchID     uint            `json:"batch_id"`
	NodeID      uint            `json:"node_id"`
	Generation  int             `json:"generation"`
	Cutoff      time.Time       `json:"cutoff"`
	EntityRef   string          `json:"entity_ref"`
	Component   string          `json:"component"`
	Split       string          `json:"split"`
	Features    HistoryFeatures `json:"features"`
	Label       *bool           `json:"label"`
	EpisodeKeys []string        `json:"episode_keys"`
	Eligible    bool            `json:"eligible"`
	Blockers    []string        `json:"blockers"`
}
type Dataset struct {
	FeatureContract   string         `json:"feature_contract"`
	ExplorationReady  bool           `json:"exploration_ready"`
	PaperCoreComplete bool           `json:"paper_core_complete"`
	Version           string         `json:"version"`
	Target            string         `json:"target"`
	Spec              DatasetSpec    `json:"spec"`
	Schema            []FeatureField `json:"schema"`
	FeatureSHA        string         `json:"feature_sha"`
	SplitSHA          string         `json:"split_sha"`
	InputSHA          string         `json:"input_sha"`
	SHA               string         `json:"sha"`
	Rows              []DatasetRow   `json:"rows"`
	Counts            map[string]int `json:"counts"`
	Ready             bool           `json:"ready"`
	Blockers          []string       `json:"blockers"`
	Choices           []string       `json:"implementation_choices"`
}
type historyEvent struct {
	key, kind   string
	at, through time.Time
	sha         []string
}

func numberPtr(v float64) *float64 { return &v }
func historyAt(ctx context.Context, m api.GPURiskMember, q api.GPURiskQuery, evs []api.GPURiskEvidenceRevision, reviews []api.HardwareFaultFeedbackReview) (HistoryFeatures, error) {
	f := HistoryFeatures{Numeric: map[string]*float64{}, Categorical: map[string]*string{}, EvidenceSHAs: []string{}, Blockers: []string{}}
	for _, field := range CoreSchema() {
		if field.Dtype == "nullable_float64" {
			f.Numeric[field.Name] = nil
		}
		if field.Dtype == "nullable_category" {
			f.Categorical[field.Name] = nil
		}
	}
	host := m.EntityRef
	if host != "" {
		f.Categorical["host_id"] = &host
	}
	local := q.Cutoff.In(time.FixedZone("Asia/Shanghai", 8*3600))
	weekday := (int(local.Weekday()) + 6) % 7
	f.Numeric["weekday"] = numberPtr(float64(weekday))
	weekend := 0.0
	if weekday >= 5 {
		weekend = 1
	}
	f.Numeric["weekend"] = numberPtr(weekend)
	latest := map[string]api.GPURiskEvidenceRevision{}
	for _, e := range evs {
		if !e.AvailableAt.Before(q.Cutoff) {
			continue
		}
		key := e.Kind + ":" + e.Key
		if p, ok := latest[key]; !ok || e.Revision > p.Revision {
			latest[key] = e
		}
	}
	assertions := []api.GPURiskEvidenceRevision{}
	for _, e := range latest {
		assertions = append(assertions, e)
	}
	sort.Slice(assertions, func(i, j int) bool { return assertions[i].SHA < assertions[j].SHA })
	selected := map[uint]api.HardwareFaultFeedbackReview{}
	for _, r := range reviews {
		if !r.ReviewedAt.Before(q.Cutoff) || !r.CreatedAt.Before(q.Cutoff) {
			continue
		}
		old, ok := selected[r.FeedbackRequestID]
		if !ok || r.Revision > old.Revision {
			selected[r.FeedbackRequestID] = r
		}
	}
	excluded := map[string]bool{}
	for _, r := range selected {
		if r.Decision == "excluded_no_hardware" && r.EpisodeKey != "" {
			excluded[r.EpisodeKey] = true
		}
	}
	events := map[string]historyEvent{}
	conflict := false
	var intervals [][2]time.Time
	var incomplete [][2]time.Time
	for index, e := range assertions {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return f, err
			}
		}
		var v EvidenceInput
		if json.Unmarshal([]byte(e.Data), &v) != nil {
			return f, ErrInput
		}
		if v.EntityRef != m.EntityRef {
			continue
		}
		if v.Kind == "coverage" && v.Source == "hardware_fault_ledger" && !v.Complete {
			incomplete = append(incomplete, [2]time.Time{v.From, v.Through})
		}
		if v.Kind == "coverage" && v.Source == "hardware_fault_ledger" && v.Complete {
			end := v.Through
			if end.After(q.Cutoff) {
				end = q.Cutoff
			}
			if end.After(v.From) {
				intervals = append(intervals, [2]time.Time{v.From, end})
				f.EvidenceSHAs = append(f.EvidenceSHAs, e.SHA)
			}
		}
		if v.Kind != "identity" {
			continue
		}
		r, ok := selected[v.FeedbackID]
		if !ok || r.ReviewSHA256 != v.ReviewSHA || r.Decision != "confirmed_hardware" || r.EpisodeKey == "" || len(r.EvidenceKeys) == 0 {
			continue
		}
		a, b := reviewWindow(r)
		if a.IsZero() || !b.Before(q.Cutoff) || a.After(b) || v.From.After(a) || v.Through.Before(b) {
			continue
		}
		if excluded[r.EpisodeKey] {
			conflict = true
			continue
		}
		if prior, ok := events[r.EpisodeKey]; ok && (!prior.at.Equal(a) || !prior.through.Equal(b) || prior.kind != r.ConfirmedFaultType) {
			conflict = true
			continue
		}
		shas := append(events[r.EpisodeKey].sha, r.ReviewSHA256, e.SHA)
		events[r.EpisodeKey] = historyEvent{r.EpisodeKey, r.ConfirmedFaultType, a, b, shas}
	}
	sorted := []historyEvent{}
	for _, e := range events {
		sorted = append(sorted, e)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].at.Equal(sorted[j].at) {
			return sorted[i].key < sorted[j].key
		}
		return sorted[i].at.Before(sorted[j].at)
	})
	// Merge complete as-known source intervals; exposure ends at complete_through.
	// R3-C permits a bounded unknown tail [complete_through, cutoff] of at most
	// MaxSourceLag. It does not extend observed exposure or relax AvailableAt < cutoff.
	sort.Slice(intervals, func(i, j int) bool { return intervals[i][0].Before(intervals[j][0]) })
	var start, end time.Time
	for _, x := range intervals {
		if end.IsZero() || x[0].After(end) {
			start, end = x[0], x[1]
		} else if x[1].After(end) {
			end = x[1]
		}
	}
	for _, bad := range incomplete {
		if !end.IsZero() && bad[0].Before(end) && bad[1].After(start) {
			start = time.Time{}
			end = time.Time{}
			f.Blockers = append(f.Blockers, "history_coverage_conflict")
			break
		}
	}
	covered := !end.IsZero() && !end.Before(q.Cutoff.Add(-MaxSourceLag))
	f.HistoryUnknown = !covered
	if !end.IsZero() {
		days := end.Sub(start).Hours() / 24
		f.HistoryCoverageDays = numberPtr(days)
	}
	if !covered {
		f.Blockers = append(f.Blockers, "history_coverage_unknown")
	}
	// Counts represent observed, independently confirmed episodes; coverage is
	// separately exposed. Zero is known only under an explicit observation window.
	if len(sorted) > 0 || covered {
		f.Numeric["episode_count"] = numberPtr(float64(len(sorted)))
		recent := 0
		recentAmbiguous, intervalAmbiguous := false, false
		f.ByFaultType = map[string]int{}
		for _, e := range sorted {
			if !e.at.Equal(e.through) {
				intervalAmbiguous = true
			}
			if e.at.Before(q.Cutoff.Add(-30*24*time.Hour)) && !e.through.Before(q.Cutoff.Add(-30*24*time.Hour)) {
				recentAmbiguous = true
			}
			if !e.at.Before(q.Cutoff.Add(-30 * 24 * time.Hour)) {
				recent++
			}
			f.ByFaultType[e.kind]++
			f.EvidenceSHAs = append(f.EvidenceSHAs, e.sha...)
		}
		noHistory := len(sorted) == 0
		f.NoHistory = &noHistory
		if !recentAmbiguous && (len(sorted) > 0 || covered && !start.After(q.Cutoff.Add(-30*24*time.Hour))) {
			f.Numeric["episode_count_30d"] = numberPtr(float64(recent))
		}
		if len(sorted) > 0 && !recentAmbiguous {
			f.Numeric["recent_ratio"] = numberPtr(float64(recent) / float64(len(sorted)))
		} else if covered && !recentAmbiguous {
			f.Numeric["recent_ratio"] = numberPtr(0)
		}
		if len(sorted) >= 2 && !intervalAmbiguous {
			f.Numeric["mean_interval_days"] = numberPtr(sorted[len(sorted)-1].at.Sub(sorted[0].at).Hours() / 24 / float64(len(sorted)-1))
		}
		if f.HistoryCoverageDays != nil && *f.HistoryCoverageDays > 0 {
			observed := 0
			ambiguousExposure := false
			for _, e := range sorted {
				if e.at.Before(start) && !e.through.Before(start) || !e.at.After(end) && e.through.After(end) {
					ambiguousExposure = true
				}
				if !e.at.Before(start) && !e.through.After(end) {
					observed++
				}
			}
			if !ambiguousExposure {
				f.Numeric["frequency_per_day"] = numberPtr(float64(observed) / *f.HistoryCoverageDays)
			}
		}
	}
	if conflict {
		f.Blockers = append(f.Blockers, "historical_episode_conflict")
	}
	applyHistorySources(&f, m, q, assertions, sorted)
	sort.Strings(f.EvidenceSHAs)
	return f, nil
}

type components map[string]string

func (c components) root(k string) string {
	if _, ok := c[k]; !ok {
		c[k] = k
	}
	if c[k] != k {
		c[k] = c.root(c[k])
	}
	return c[k]
}
func (c components) join(a, b string) {
	a, b = c.root(a), c.root(b)
	if a < b {
		c[b] = a
	} else {
		c[a] = b
	}
}
func componentSplit(component string) string {
	h := sha256.Sum256([]byte(component))
	if h[0]%10 < 6 {
		return "train"
	}
	if h[0]%10 < 8 {
		return "validation"
	}
	return "test"
}

// BuildDataset exports a read-only immutable file artifact. Unknown labels and
// rejected rows are retained; readiness never substitutes row count for episodes.
func BuildDataset(ctx context.Context, db *gorm.DB, spec DatasetSpec) (Dataset, error) {
	out := Dataset{FeatureContract: HistoryFeatureVersion, Version: DatasetVersion, Target: Target, Spec: spec, Schema: CoreSchema(), Rows: []DatasetRow{}, Counts: map[string]int{}, Blockers: []string{}, Choices: []string{CoverageWatermarkContract, "paper_core only; unavailable source fields remain null", "3-day sampling from explicit UTC anchor; earliest pinned query per UTC date", "deterministic connected-component 60/20/20 assignment; time purge >=8d", "no preprocessing fit, no balancing, no training"}}
	if spec.Profile != "paper_core" || len(spec.BatchIDs) == 0 || len(spec.BatchIDs) > 200 || spec.Anchor.IsZero() || !spec.Anchor.Before(spec.TrainEnd) || !spec.TrainEnd.Before(spec.ValidationEnd) || !spec.ValidationEnd.Before(spec.TestEnd) || spec.TestEnd.After(time.Now()) {
		return out, ErrInput
	}
	ids := append([]uint{}, spec.BatchIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if id == 0 || i > 0 && ids[i-1] == id {
			return out, ErrInput
		}
	}
	spec.BatchIDs = ids
	spec.Anchor = spec.Anchor.UTC()
	spec.TrainEnd = spec.TrainEnd.UTC()
	spec.ValidationEnd = spec.ValidationEnd.UTC()
	spec.TestEnd = spec.TestEnd.UTC()
	out.Spec = spec
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var evidence []api.GPURiskEvidenceRevision
		var reviews []api.HardwareFaultFeedbackReview
		if e := tx.Order("id").Limit(100001).Find(&evidence).Error; e != nil {
			return e
		}
		if e := tx.Order("feedback_request_id,revision").Limit(100001).Find(&reviews).Error; e != nil {
			return e
		}
		if len(evidence) > 100000 || len(reviews) > 100000 {
			return ErrInput
		}
		graph := components{}
		inputs := []any{}
		type rowKey struct {
			query, node uint
			generation  int
		}
		gpuComplete := map[rowKey]bool{}
		seenPinnedQuery := map[uint]bool{}
		queryDay := map[string]uint{}
		queryTimes := map[uint]time.Time{}
		var maxAsOf time.Time
		for _, id := range ids {
			var batch api.GPURiskOutcomeBatch
			if e := tx.First(&batch, id).Error; e != nil {
				return e
			}
			batch.AsOf = batch.AsOf.UTC()
			if seenPinnedQuery[batch.QueryID] {
				return ErrInput
			}
			seenPinnedQuery[batch.QueryID] = true
			if batch.AsOf.After(maxAsOf) {
				maxAsOf = batch.AsOf
			}
			if batch.Version != OutcomeVersion {
				return ErrInput
			}
			var q api.GPURiskQuery
			if e := tx.First(&q, batch.QueryID).Error; e != nil {
				return e
			}
			q.Cutoff = q.Cutoff.UTC()
			if q.Contract != Contract || q.Target != Target || q.Status != "completed" || q.HorizonMinutes != 10080 {
				return ErrInput
			}
			var scope api.GPURiskScope
			if e := tx.Where("sha = ?", q.ScopeSHA).First(&scope).Error; e != nil {
				return e
			}
			var snapshot Snapshot
			if json.Unmarshal([]byte(scope.Input), &snapshot) != nil {
				return ErrInput
			}
			if snapshot.SHA != q.ScopeSHA || !snapshot.Cutoff.Equal(q.Cutoff) {
				return ErrInput
			}
			if e := validate(snapshot, time.Now()); e != nil {
				return e
			}
			var members []api.GPURiskMember
			var labels []api.GPURiskNodeOutcome
			if e := tx.Where("query_id = ?", q.ID).Order("node_id,generation").Find(&members).Error; e != nil {
				return e
			}
			if e := tx.Where("batch_id = ?", batch.ID).Order("node_id,generation").Find(&labels).Error; e != nil {
				return e
			}
			if len(out.Rows)+len(members) > 100000 {
				return ErrInput
			}
			if len(members) != q.MemberCount || len(labels) != q.MemberCount || batch.MemberCount != q.MemberCount {
				return ErrInput
			}
			expectedMembers, expectedScores, e := Build(snapshot)
			if e != nil {
				return e
			}
			resultSHA, _ := digest([]any{expectedMembers, expectedScores})
			if resultSHA != q.ResultSHA {
				return fmt.Errorf("%w: ranking digest", ErrInput)
			}
			normalizedMembers := append([]api.GPURiskMember{}, members...)
			for i := range normalizedMembers {
				normalizedMembers[i].ID = 0
				normalizedMembers[i].QueryID = 0
			}
			a, _ := digest(expectedMembers)
			b, _ := digest(normalizedMembers)
			if a != b {
				return fmt.Errorf("%w: member digest", ErrInput)
			}
			normalizedLabels := append([]api.GPURiskNodeOutcome{}, labels...)
			for i := range normalizedLabels {
				normalizedLabels[i].ID = 0
				normalizedLabels[i].BatchID = 0
			}
			outcomeSHA, _ := digest([]any{OutcomeVersion, q.Key, batch.AsOf, batch.InputSHA, normalizedLabels})
			if outcomeSHA != batch.SHA {
				return fmt.Errorf("%w: outcome digest", ErrInput)
			}
			inputs = append(inputs, []any{q.Key, q.ScopeSHA, q.ResultSHA, batch.SHA})
			day := q.Cutoff.UTC().Format("2006-01-02")
			queryTimes[q.ID] = q.Cutoff
			if old, ok := queryDay[day]; !ok || q.Cutoff.Before(queryTimes[old]) || q.Cutoff.Equal(queryTimes[old]) && q.ID < old {
				queryDay[day] = q.ID
			}
			for _, n := range snapshot.Nodes {
				memberKey := ""
				for _, m := range members {
					if m.NodeID == n.ID && m.Generation == n.Generation {
						memberKey = m.EntityRef
						break
					}
				}
				if memberKey == "" {
					continue
				}
				graph.root(memberKey)
				complete := len(n.Cards) > 0 && n.Expected == len(n.Cards) && n.Observed <= len(n.Cards)
				for _, card := range n.Cards {
					if card.UUID == "" || card.State != "active" || strings.HasPrefix(strings.ToLower(card.UUID), "mig") {
						complete = false
						continue
					}
					graph.join(memberKey, "gpu:"+card.UUID)
				}
				gpuComplete[rowKey{q.ID, n.ID, n.Generation}] = complete
			}
			for i, m := range members {
				label := labels[i]
				if label.NodeID != m.NodeID || label.Generation != m.Generation {
					return ErrInput
				}
				features, e := historyAt(ctx, m, q, evidence, reviews)
				if e != nil {
					return e
				}
				row := DatasetRow{QueryID: q.ID, BatchID: batch.ID, NodeID: m.NodeID, Generation: m.Generation, Cutoff: q.Cutoff, EntityRef: m.EntityRef, Features: features, Label: label.Positive, EpisodeKeys: append([]string{}, label.EpisodeKeys...), Blockers: []string{}}
				if m.Status != "included" {
					row.Blockers = append(row.Blockers, "scope:"+m.Reason)
				}
				if label.Status != "matured_positive" && label.Status != "matured_negative" || label.Intervention {
					row.Blockers = append(row.Blockers, "label:"+label.Status)
				}
				if label.Positive == nil {
					row.Blockers = append(row.Blockers, "unknown_label")
				}
				if q.Cutoff.Before(spec.Anchor) || !q.Cutoff.Before(spec.TestEnd) {
					row.Blockers = append(row.Blockers, "outside_time_range")
				}
				days := int(q.Cutoff.Sub(spec.Anchor).Hours() / 24)
				if days < 0 || days%3 != 0 {
					row.Blockers = append(row.Blockers, "query_cadence")
				}
				boundary := spec.TrainEnd
				row.Split = "train"
				if !q.Cutoff.Before(spec.TrainEnd) {
					row.Split = "validation"
					boundary = spec.ValidationEnd
				}
				if !q.Cutoff.Before(spec.ValidationEnd) {
					row.Split = "test"
					boundary = spec.TestEnd
				}
				if q.Cutoff.Add(8*24*time.Hour).After(boundary) || batch.AsOf.After(boundary) {
					row.Blockers = append(row.Blockers, "label_purge_or_late_availability")
				}
				for _, key := range label.EpisodeKeys {
					if m.EntityRef != "" {
						graph.join(m.EntityRef, "episode:"+key)
					}
				}
				out.Rows = append(out.Rows, row)
			}
		}
		graphEvidence := []string{}
		for _, e := range evidence {
			if e.Kind != "identity" || e.AvailableAt.After(maxAsOf) {
				continue
			}
			var v EvidenceInput
			if json.Unmarshal([]byte(e.Data), &v) != nil {
				return ErrInput
			}
			if v.EntityRef != "" && v.GPUUUID != "" {
				graph.join(v.EntityRef, "gpu:"+v.GPUUUID)
				graphEvidence = append(graphEvidence, e.SHA)
			}
		}
		sort.Strings(graphEvidence)
		sort.Slice(out.Rows, func(i, j int) bool {
			a, b := out.Rows[i], out.Rows[j]
			if !a.Cutoff.Equal(b.Cutoff) {
				return a.Cutoff.Before(b.Cutoff)
			}
			if a.QueryID != b.QueryID {
				return a.QueryID < b.QueryID
			}
			return a.NodeID < b.NodeID
		})
		seenQuery := map[uint]bool{}
		for i := range out.Rows {
			r := &out.Rows[i]
			if r.EntityRef == "" {
				r.Blockers = append(r.Blockers, "identity_unknown")
			} else {
				root := graph.root(r.EntityRef)
				hash := sha256.Sum256([]byte(root))
				r.Component = hex.EncodeToString(hash[:])
				if componentSplit(root) != r.Split {
					r.Blockers = append(r.Blockers, "entity_partition")
				}
			}
			if !gpuComplete[rowKey{r.QueryID, r.NodeID, r.Generation}] {
				r.Blockers = append(r.Blockers, "gpu_identity_incomplete")
			}
			if queryDay[r.Cutoff.UTC().Format("2006-01-02")] != r.QueryID {
				r.Blockers = append(r.Blockers, "duplicate_query_date")
			}
			// Core can be exported with null fields, but incomplete semantics are not a
			// paper-compatible training certification. Readiness exposes this blocker.
			if contains(r.Features.Blockers, "historical_episode_conflict") {
				r.Blockers = append(r.Blockers, "core_feature_contract_incomplete")
			}
			r.Eligible = len(r.Blockers) == 0
			out.Counts["rows"]++
			if !seenQuery[r.QueryID] {
				out.Counts["queries"]++
				seenQuery[r.QueryID] = true
			}
			if r.Eligible {
				out.Counts[r.Split+"_eligible"]++
			} else {
				out.Counts["excluded"]++
			}
		}
		featureRows, e := coreFeatureRows(out)
		if e != nil {
			return e
		}
		out.InputSHA, e = digest([]any{inputs, featureRows, graphEvidence})
		if e != nil {
			return e
		}
		out.FeatureSHA, e = digest([]any{out.Schema, featureRows})
		if e != nil {
			return e
		}
		out.SplitSHA, e = digest([]any{spec, graph, out.Rows})
		return e
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	out.PaperCoreComplete = true
	positiveNodes := map[string]map[string]bool{}
	controls := map[string]map[string]bool{}
	episodes := map[string]map[string]bool{}
	for _, split := range []string{"train", "validation", "test"} {
		positiveNodes[split] = map[string]bool{}
		controls[split] = map[string]bool{}
		episodes[split] = map[string]bool{}
	}
	for _, r := range out.Rows {
		if len(r.Features.Blockers) > 0 {
			out.PaperCoreComplete = false
		}
		if !r.Eligible || r.Label == nil {
			continue
		}
		if *r.Label {
			positiveNodes[r.Split][r.Component] = true
			for _, ep := range r.EpisodeKeys {
				episodes[r.Split][ep] = true
			}
		} else {
			controls[r.Split][r.Component] = true
		}
	}
	out.Ready = true
	for _, split := range []string{"train", "validation", "test"} {
		out.Counts[split+"_positive_components"] = len(positiveNodes[split])
		out.Counts[split+"_control_components"] = len(controls[split])
		out.Counts[split+"_independent_episodes"] = len(episodes[split])
		minEP, minPositive, minControl := 10, 5, 20
		if split == "train" {
			minEP, minPositive, minControl = 30, 10, 60
		}
		if len(episodes[split]) < minEP || len(positiveNodes[split]) < minPositive || len(controls[split]) < minControl {
			out.Ready = false
			out.Blockers = append(out.Blockers, split+":independent_episode_readiness_not_met")
		}
	}
	out.ExplorationReady = out.Ready
	out.Ready = out.ExplorationReady && out.PaperCoreComplete
	if !out.PaperCoreComplete {
		out.Blockers = append(out.Blockers, "paper_core_source_fields_incomplete")
	}

	sha, e := digest(out)
	out.SHA = sha
	return out, e
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
