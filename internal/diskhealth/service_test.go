package diskhealth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"atlas/pkg/storage"
)

func diskServiceReport(t *testing.T, observed time.Time, mediaErrors string) Report {
	t.Helper()
	in := sampleInput()
	in.ObservedAt = observed
	in.AvailableAt = observed.Add(5 * time.Second)
	data := sampleJSON()
	data["nvme_smart_health_information_log"].(map[string]any)["media_errors"] = json.Number(mediaErrors)
	row := Assess(in, marshal(t, data), nil, observed.Add(time.Minute))
	summary := Summary{Total: 1, ByLevel: map[string]int{row.Level: 1}}
	if row.AssessmentStatus == "assessed" {
		summary.Assessed = 1
	} else {
		summary.Unknown = 1
	}
	return Report{
		Schema:              SchemaVersion,
		InputManifestSHA256: Digest([]byte("manifest-" + observed.Format(time.RFC3339Nano))),
		RuleVersion:         ReportRuleVersion,
		Mode:                "offline_replay",
		AsOf:                observed.Add(time.Minute),
		Rows:                []Result{row},
		Summary:             summary,
	}
}

func newDiskServiceTestDB(t *testing.T) (*storage.DB, *Service) {
	t.Helper()
	db, err := storage.InitDB(filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db, NewService(db)
}

func TestDiskHealthPersistenceCurrentHistoryAndEpisodes(t *testing.T) {
	_, service := newDiskServiceTestDB(t)
	firstAt := cutoff.Add(-2 * time.Hour)
	first := diskServiceReport(t, firstAt, "0")
	created, err := service.PersistReport(first, "")
	if err != nil {
		t.Fatal(err)
	}
	if created.SnapshotsCreated != 1 || created.CurrentPromoted != 1 {
		t.Fatalf("first persist did not create current snapshot: %+v", created)
	}
	duplicate, err := service.PersistReport(first, "")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.SnapshotsCreated != 0 || duplicate.SnapshotsDuplicate != 1 || duplicate.EpisodesUpserted != 0 {
		t.Fatalf("duplicate import was not idempotent: %+v", duplicate)
	}

	second := diskServiceReport(t, cutoff.Add(-time.Hour), "2")
	updated, err := service.PersistReport(second, "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.SnapshotsCreated != 1 || updated.CurrentPromoted != 1 || updated.EpisodesUpserted != 1 {
		t.Fatalf("second persist did not promote and create episode: %+v", updated)
	}
	reimport, err := service.PersistReport(second, "")
	if err != nil {
		t.Fatal(err)
	}
	if reimport.EpisodesUpserted != 0 {
		t.Fatalf("episode count changed on duplicate import: %+v", reimport)
	}

	rows, total, err := service.ListCurrentSnapshots(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].Level != "attention" || rows[0].Current != true {
		t.Fatalf("unexpected current list: total=%d rows=%+v", total, rows)
	}
	detail, err := service.Detail(rows[0].EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Episodes) != 1 || detail.Episodes[0].RuleCode != "historical_media_errors" || detail.Episodes[0].OccurrenceCount != 1 {
		t.Fatalf("episode merge failed: %+v", detail.Episodes)
	}
	history, err := service.History(rows[0].EntityID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Rows) != 2 || history.Total != 2 || history.Windows[1].Window != "24h" || history.Windows[1].SampleCount != 2 || history.Windows[1].CoverageStatus != "available" {
		t.Fatalf("history window missing two samples: %+v", history)
	}
	pagedHistory, err := service.History(rows[0].EntityID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pagedHistory.Rows) != 1 || pagedHistory.Total != 2 {
		t.Fatalf("history pagination lost total count: %+v", pagedHistory)
	}
}

func TestDiskHealthHandlerIsReadOnlyListAndHistory(t *testing.T) {
	_, service := newDiskServiceTestDB(t)
	report := diskServiceReport(t, cutoff.Add(-time.Hour), "3")
	if _, err := service.PersistReport(report, ""); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(service)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hardware/disks?level=attention", nil)
	handler.HandleDisks(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list failed: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Data []SnapshotView `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Meta.Total != 1 || len(list.Data) != 1 || list.Data[0].Source != "smartctl" {
		t.Fatalf("unexpected list payload: %+v", list)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/hardware/disks/"+list.Data[0].EntityID+"/history?limit=1", nil)
	handler.HandleDiskSubresource(rec, req)
	if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("history failed: %d %s", rec.Code, rec.Body.String())
	}
	var history struct {
		Data []SnapshotView `json:"data"`
		Meta struct {
			Total int `json:"total"`
			Limit int `json:"limit"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Data) != 1 || history.Meta.Total != 1 || history.Meta.Limit != 1 {
		t.Fatalf("unexpected history payload: %+v", history)
	}
}

func TestObservationAgeDoesNotRewriteHistoricalAssessment(t *testing.T) {
	_, service := newDiskServiceTestDB(t)
	observed := cutoff.Add(-2 * time.Hour)
	report := diskServiceReport(t, observed, "0")
	service.now = func() time.Time { return cutoff }
	if _, err := service.PersistReport(report, ""); err != nil {
		t.Fatal(err)
	}
	rows, _, err := service.ListCurrentSnapshots(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Observation.State != "stale" || rows[0].Freshness != "fresh_at_cutoff" || rows[0].AssessmentStatus != "assessed" {
		t.Fatalf("historical and live age confused: %+v", rows[0])
	}
	summary, err := service.Summary()
	if err != nil || summary.StaleCount != 1 {
		t.Fatalf("stale count: %+v %v", summary, err)
	}
	detail, err := service.Detail(rows[0].EntityID)
	if err != nil || detail.Observation.State != "stale" {
		t.Fatalf("detail age: %+v %v", detail, err)
	}
	history, err := service.History(rows[0].EntityID, 1, 0)
	if err != nil || history.Rows[0].Observation.State != "stale" {
		t.Fatalf("history age: %+v %v", history, err)
	}
	service.now = func() time.Time { return observed.Add(time.Hour) }
	rows, _, _ = service.ListCurrentSnapshots(ListOptions{})
	if rows[0].Observation.State != "fresh" {
		t.Fatal("exact age boundary rejected")
	}
	for _, test := range []struct {
		observed, available time.Time
		want                string
	}{
		{time.Time{}, cutoff, "unknown"}, {cutoff, cutoff.Add(-time.Second), "unknown"},
		{cutoff, cutoff.Add(time.Second), "future"}, {cutoff.Add(time.Second), cutoff.Add(time.Second), "future"},
	} {
		if got := observationStatus(test.observed, test.available, cutoff); got.State != test.want {
			t.Fatalf("got %+v want %s", got, test.want)
		}
	}
}
