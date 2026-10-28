package gpurisk

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
)

func sourceInput(q api.GPURiskQuery) EvidenceInput {
	return EvidenceInput{Kind: "host_context", Key: "context", Owner: "fixture", SourceVersion: HistorySourceVersion, EvidenceSHA: strings.Repeat("c", 64), NodeID: 1, EntityRef: "serial:sn-1", From: q.Cutoff.Add(-time.Hour), Through: q.Cutoff.Add(-time.Minute), State: "active", Context: &HostContext{HostType: "gpu_host", GPUModel: "fixture-model", Manufacturer: "fixture-vendor", MainBoard: "fixture-board", QuotaGroup: "fixture-quota", DataCenter: "fixture-dc", Rack: "fixture-rack"}}
}
func sourceRow(v EvidenceInput, at time.Time, revision int) api.GPURiskEvidenceRevision {
	b, _ := json.Marshal(v)
	return api.GPURiskEvidenceRevision{Kind: v.Kind, Key: v.Key, Revision: revision, AvailableAt: at, Data: string(b), SHA: strings.Repeat(string(rune('a'+revision)), 64)}
}

func TestHistorySourceValidationCASAndAPI(t *testing.T) {
	db, q, _ := outcomeDB(t)
	v := sourceInput(q)
	first := appendInput(t, db, v)
	if first.AvailableAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatal("backdated availability")
	}
	for _, change := range []func(*EvidenceInput){
		func(v *EvidenceInput) { v.SourceVersion = "bad" }, func(v *EvidenceInput) { v.State = "recovered" }, func(v *EvidenceInput) { v.Context = nil }, func(v *EvidenceInput) { v.QueryID = q.ID }, func(v *EvidenceInput) { v.Severity = "critical" }, func(v *EvidenceInput) { v.Complete = true },
	} {
		bad := v
		change(&bad)
		if _, e := AppendEvidence(context.Background(), db, bad); e == nil {
			t.Fatal("invalid source accepted", bad)
		}
	}
	v.PreviousSHA = first.SHA
	v.State = "withdrawn"
	v.Context = nil
	withdrawn := appendInput(t, db, v)
	if withdrawn.Revision != 2 {
		t.Fatal(withdrawn)
	}
	v.EntityRef = "serial:other"
	v.PreviousSHA = withdrawn.SHA
	if _, e := AppendEvidence(context.Background(), db, v); e == nil {
		t.Fatal("chain identity changed")
	}
	f, r := seedReview(t, db, q, "state-source", -24*time.Hour)
	state := sourceInput(q)
	state.Kind = "episode_state"
	state.Key = "state"
	state.Context = nil
	state.FeedbackID = f.ID
	state.ReviewSHA = r.ReviewSHA256
	state.State = "unresolved"
	state.Severity = "critical"
	appendInput(t, db, state)
	state.PreviousSHA = ""
	state.Key = "invalid"
	state.Severity = "XID94"
	if _, e := AppendEvidence(context.Background(), db, state); e == nil {
		t.Fatal("guessed severity accepted")
	}
	state.Severity = "critical"
	state.From = q.Cutoff.Add(-48 * time.Hour)
	if _, e := AppendEvidence(context.Background(), db, state); e == nil {
		t.Fatal("state before onset accepted")
	}
	for _, kind := range []string{"host_context", "episode_state"} {
		rr := httptest.NewRecorder()
		Handler{DB: db}.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/gpu-risk/evidence?kind="+kind, nil))
		if rr.Code != 200 {
			t.Fatal(rr.Code, rr.Body.String())
		}
	}
}

func TestHistorySourcesCutoffConflictWithdrawalAndUnknown(t *testing.T) {
	q := api.GPURiskQuery{Cutoff: time.Now().UTC().Add(-time.Hour)}
	m := api.GPURiskMember{NodeID: 1, EntityRef: "serial:sn-1"}
	contextInput := sourceInput(q)
	known := q.Cutoff.Add(-time.Minute)
	contextRow := sourceRow(contextInput, known, 1)
	state := contextInput
	state.Kind = "episode_state"
	state.Key = "state"
	state.Context = nil
	state.ReviewSHA = strings.Repeat("f", 64)
	state.State = "unresolved"
	state.Severity = "critical"
	stateRow := sourceRow(state, known, 1)
	events := []historyEvent{{key: "one", sha: []string{state.ReviewSHA}}}
	feature := func(rows []api.GPURiskEvidenceRevision, events []historyEvent) HistoryFeatures {
		f := HistoryFeatures{Numeric: map[string]*float64{"episode_count": numberPtr(1)}, Categorical: map[string]*string{}, EvidenceSHAs: []string{}, Blockers: []string{}}
		applyHistorySources(&f, m, q, rows, events)
		return f
	}
	f := feature([]api.GPURiskEvidenceRevision{contextRow, stateRow}, events)
	if len(f.Blockers) != 0 || f.BySeverity["critical"] != 1 || *f.Numeric["unresolved_count"] != 1 || *f.Categorical["gpu_model"] != "fixture-model" {
		t.Fatal(f)
	}
	state.State = "resolved"
	resolved := sourceRow(state, known, 2)
	f = feature([]api.GPURiskEvidenceRevision{contextRow, resolved}, events)
	if *f.Numeric["unresolved_count"] != 0 {
		t.Fatal("explicit resolution ignored", f)
	}
	f = feature([]api.GPURiskEvidenceRevision{contextRow, stateRow, resolved}, events)
	if f.Numeric["unresolved_count"] != nil || f.BySeverity != nil || !contains(f.Blockers, "episode_state_conflict") {
		t.Fatal("conflict silently chosen", f)
	}
	state.State = "withdrawn"
	state.Severity = ""
	withdrawn := sourceRow(state, known, 3)
	f = feature([]api.GPURiskEvidenceRevision{contextRow, withdrawn}, events)
	if f.Numeric["unresolved_count"] != nil || !contains(f.EvidenceSHAs, withdrawn.SHA) {
		t.Fatal("withdrawal fabricated known state", f)
	}
	changed := contextInput
	c := *changed.Context
	c.GPUModel = "other"
	changed.Context = &c
	f = feature([]api.GPURiskEvidenceRevision{contextRow, sourceRow(changed, known, 2), stateRow}, events)
	if f.Categorical["gpu_model"] != nil || !contains(f.Blockers, "host_context_conflict:gpu_model") {
		t.Fatal("context conflict chosen", f)
	}
	changed = contextInput
	changed.Generation = 1
	f = feature([]api.GPURiskEvidenceRevision{sourceRow(changed, known, 2)}, events)
	if f.Categorical["gpu_model"] != nil {
		t.Fatal("different generation leaked")
	}
	// Exercise historyAt's actual revision selection: equal/late availability and
	// late withdrawals must not rewrite a prediction's known static context.
	late := contextInput
	late.State = "withdrawn"
	late.Context = nil
	f, e := historyAt(context.Background(), m, q, []api.GPURiskEvidenceRevision{contextRow, sourceRow(late, q.Cutoff, 2)}, nil)
	if e != nil || f.Categorical["gpu_model"] == nil {
		t.Fatal("equal cutoff revision leaked", f, e)
	}
	f, e = historyAt(context.Background(), m, q, []api.GPURiskEvidenceRevision{contextRow, sourceRow(late, known, 2)}, nil)
	if e != nil || f.Categorical["gpu_model"] != nil {
		t.Fatal("withdrawal ignored", f, e)
	}
}

func TestHistorySourceReviewIdentityIntegration(t *testing.T) {
	db, q, _ := outcomeDB(t)
	f, r := seedReview(t, db, q, "historical-state", -24*time.Hour)
	old := q.Cutoff.Add(-time.Minute)
	r.ReviewedAt = old
	r.CreatedAt = old
	identityInput := sourceInput(q)
	identityInput.Kind = "identity"
	identityInput.SourceVersion = ""
	identityInput.Key = "identity"
	identityInput.State = ""
	identityInput.Context = nil
	identityInput.From = q.Cutoff.Add(-48 * time.Hour)
	identityInput.FeedbackID = f.ID
	identityInput.ReviewSHA = r.ReviewSHA256
	identityRow := sourceRow(identityInput, old, 1)
	source := sourceInput(q)
	state := source
	state.Kind = "episode_state"
	state.Key = "state"
	state.Context = nil
	state.FeedbackID = f.ID
	state.ReviewSHA = r.ReviewSHA256
	state.State = "unresolved"
	state.Severity = "critical"
	member := api.GPURiskMember{NodeID: 1, EntityRef: "serial:sn-1"}
	rows := []api.GPURiskEvidenceRevision{identityRow, sourceRow(source, old, 1), sourceRow(state, old, 1)}
	features, e := historyAt(context.Background(), member, q, rows, []api.HardwareFaultFeedbackReview{r})
	if e != nil || features.BySeverity["critical"] != 1 || features.Numeric["unresolved_count"] == nil || *features.Numeric["unresolved_count"] != 1 {
		t.Fatal("review-bound state not consumed", features, e)
	}

	if path := os.Getenv("ATLAS_GPU_RISK_HISTORY_SOURCE_FIXTURE"); path != "" {
		data, _ := json.MarshalIndent(map[string]any{"source": "synthetic_fixture_not_production_efficacy", "source_contract": HistorySourceVersion, "feature_contract": HistoryFeatureVersion, "features": features}, "", "  ")
		if e := os.WriteFile(path, append(data, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
	before, _ := digest(features)
	state.State = "resolved"
	rows = append(rows, sourceRow(state, q.Cutoff, 2))
	again, e := historyAt(context.Background(), member, q, rows, []api.HardwareFaultFeedbackReview{r})
	after, _ := digest(again)
	if e != nil || before != after {
		t.Fatal("late resolution rewrote cutoff", again, e)
	}
	rows[len(rows)-1].AvailableAt = old
	resolved, e := historyAt(context.Background(), member, q, rows, []api.HardwareFaultFeedbackReview{r})
	if e != nil || resolved.Numeric["unresolved_count"] == nil || *resolved.Numeric["unresolved_count"] != 0 {
		t.Fatal("known explicit resolution ignored", resolved, e)
	}
	state.ReviewSHA = strings.Repeat("0", 64)
	rows[len(rows)-1] = sourceRow(state, old, 2)
	unknown, e := historyAt(context.Background(), member, q, rows, []api.HardwareFaultFeedbackReview{r})
	if e != nil || unknown.Numeric["unresolved_count"] != nil || unknown.BySeverity != nil {
		t.Fatal("unmatched review state leaked", unknown, e)
	}
}
