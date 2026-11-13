package gpurisk

import (
	"atlas/internal/history"
	"atlas/pkg/api"
	"atlas/pkg/config"
	"atlas/pkg/storage"
	"context"
	"testing"
	"time"
)

func TestHistoryAuditBindingPreservesFixedCutoffFeatureAndOutcomeSHA(t *testing.T) {
	db, q, _ := outcomeDB(t)
	if e := db.AutoMigrate(&api.MonitoringHistoryAudit{}, &api.GPURiskSourceCoverageCheck{}); e != nil {
		t.Fatal(e)
	}
	// Seed explicit pre-cutoff context so the proof covers nonempty historical
	// source content, not merely a calendar-only or zero-evidence feature vector.
	source := sourceRow(sourceInput(q), q.Cutoff.Add(-time.Minute), 1)
	if e := db.Create(&source).Error; e != nil {
		t.Fatal(e)
	}
	var member api.GPURiskMember
	if e := db.Where("query_id = ? AND node_id = ?", q.ID, 1).First(&member).Error; e != nil {
		t.Fatal(e)
	}
	historySHA := func() string {
		var evs []api.GPURiskEvidenceRevision
		var reviews []api.HardwareFaultFeedbackReview
		if e := db.Order("id").Find(&evs).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Order("id").Find(&reviews).Error; e != nil {
			t.Fatal(e)
		}
		f, e := historyAt(context.Background(), member, q, evs, reviews)
		if e != nil {
			t.Fatal(e)
		}
		if f.Categorical["gpu_model"] == nil || *f.Categorical["gpu_model"] != "fixture-model" {
			t.Fatal("fixture did not exercise real pre-cutoff context", f)
		}
		v, e := digest([]any{member.NodeID, member.Generation, q.Cutoff, f})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	beforeFeature := historySHA()
	at := time.Now().UTC()
	local := at.In(time.FixedZone("Shanghai", 8*3600))
	if local.Hour() < 3 {
		at = at.Add(3 * time.Hour)
	}
	beforeOutcome, _, e := EvaluateOutcomes(context.Background(), db, q.ID, at)
	if e != nil {
		t.Fatal(e)
	}
	var evidenceBefore, coverageBefore int64
	if err := db.Model(&api.GPURiskEvidenceRevision{}).Count(&evidenceBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&api.GPURiskSourceCoverageCheck{}).Count(&coverageBefore).Error; err != nil {
		t.Fatal(err)
	}
	// A failed authenticated source still writes configuration provenance but
	// never signs coverage or changes historical outcome evidence.
	service := history.NewService(&storage.DB{DB: db}, config.HistoryConfig{Enabled: true, Sources: []config.HistorySourceConfig{{ID: "audit-only", Type: "victoriametrics-cluster", BaseURL: "http://unused.test", TenantID: "7", Enabled: true, AuthType: "bearer"}}}, time.Second)
	rows, err := service.AuditAll(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Status != "failed" || rows[0].Details["source_binding_version"] != "history-source-binding-v1" {
		t.Fatalf("audit rows=%+v err=%v", rows, err)
	}
	var evidenceAfter, coverageAfter int64
	if err := db.Model(&api.GPURiskEvidenceRevision{}).Count(&evidenceAfter).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&api.GPURiskSourceCoverageCheck{}).Count(&coverageAfter).Error; err != nil {
		t.Fatal(err)
	}
	if evidenceBefore != evidenceAfter || coverageBefore != coverageAfter {
		t.Fatal("history audit created coverage or evidence")
	}

	afterFeature := historySHA()
	afterOutcome, _, e := EvaluateOutcomes(context.Background(), db, q.ID, at)
	if e != nil {
		t.Fatal(e)
	}
	if beforeFeature != afterFeature || beforeOutcome.SHA != afterOutcome.SHA {
		t.Fatal("fixed historical feature or outcome changed")
	}
	var frozen api.GPURiskQuery
	if e = db.First(&frozen, q.ID).Error; e != nil {
		t.Fatal(e)
	}
	if frozen.ScopeSHA != q.ScopeSHA || frozen.ResultSHA != q.ResultSHA {
		t.Fatal("frozen ranking modified")
	}
	t.Logf("fixed_query=%d node=%d generation=%d cutoff=%s as_of=%s scope_sha=%s result_sha=%s feature_before=%s feature_after=%s outcome_before=%s outcome_after=%s", q.ID, member.NodeID, member.Generation, q.Cutoff.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), q.ScopeSHA, q.ResultSHA, beforeFeature, afterFeature, beforeOutcome.SHA, afterOutcome.SHA)
}
