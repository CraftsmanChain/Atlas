package diskhealth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas/pkg/api"
)

type installationViewFixture struct {
	service                   *Service
	old, active, unregistered Report
	scope                     api.DiskObservationScope
	closed, opened            time.Time
}

func installationViewReport(t *testing.T, key string, observed time.Time, unknown bool) Report {
	t.Helper()
	in, data := sampleInput(), sampleJSON()
	in.Reference = "installation-view-" + key
	in.ObservedAt, in.AvailableAt = observed, observed.Add(time.Second)
	data["serial_number"] = "synthetic-installation-view-" + key
	if unknown {
		delete(data, "smart_status")
	}
	row := Assess(in, marshal(t, data), nil, observed.Add(time.Minute))
	return Report{Schema: SchemaVersion, RuleVersion: ReportRuleVersion, Mode: "offline_replay", AsOf: observed.Add(time.Minute),
		InputManifestSHA256: Digest([]byte(in.Reference)), Rows: []Result{row}}
}

func newInstallationViewFixture(t *testing.T) installationViewFixture {
	t.Helper()
	_, service := newDiskServiceTestDB(t)
	service.now = func() time.Time { return cutoff }
	fixture := installationViewFixture{
		service:      service,
		old:          installationViewReport(t, "old", cutoff.Add(-3*time.Hour), false),
		active:       installationViewReport(t, "active", cutoff.Add(-2*time.Hour), true),
		unregistered: installationViewReport(t, "unregistered", cutoff.Add(-10*time.Minute), false),
		closed:       cutoff.Add(-2 * time.Hour), opened: cutoff.Add(-4 * time.Hour),
	}
	for _, report := range []Report{fixture.old, fixture.active, fixture.unregistered} {
		if _, err := service.PersistReport(report, ""); err != nil {
			t.Fatalf("persist synthetic view fixture: %v", err)
		}
	}
	asset := api.InfrastructureAsset{AssetKey: "fixture:synthetic-installation-view-host", Source: "asset_machine", EntityKind: "cpu_node", InUse: true, Present: true, IPAddress: fixture.active.Rows[0].Node}
	if err := service.db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	fixture.scope = api.DiskObservationScope{
		ScopeID: "synthetic-installation-view-scope", AssetKey: asset.AssetKey,
		HostGeneration: "host-installation-1", Node: fixture.active.Rows[0].Node,
		SnapshotScope: fixture.active.Rows[0].Scope, SnapshotGeneration: fixture.active.Rows[0].Generation,
		Disposition: "test", HostIdentityEvidenceSHA256: Digest([]byte("synthetic-host-evidence")),
		RosterVersion: 2, ManifestSHA256: Digest([]byte("synthetic-complete-roster")),
		ObservedAt: fixture.closed, AvailableAt: fixture.closed.Add(time.Second), ReconciledAt: fixture.closed.Add(time.Minute),
	}
	if err := service.db.Create(&fixture.scope).Error; err != nil {
		t.Fatal(err)
	}
	closeVersion, activeEntity := uint64(2), fixture.active.Rows[0].EntityID
	installations := []api.DiskInstallation{
		{InstallationID: "synthetic-old-installation", ScopeID: fixture.scope.ScopeID, EntityID: fixture.old.Rows[0].EntityID,
			State: "superseded", OpenedAt: fixture.opened, ClosedAt: &fixture.closed,
			OpenEvidenceSHA256: Digest([]byte("synthetic-open-old")), CloseEvidenceSHA256: fixture.scope.ManifestSHA256,
			CloseReason: "explicit_replacement", OpenRosterVersion: 1, CloseRosterVersion: &closeVersion},
		{InstallationID: "synthetic-active-installation", ScopeID: fixture.scope.ScopeID, EntityID: activeEntity,
			State: "active", ActiveEntityID: &activeEntity, OpenedAt: fixture.closed,
			OpenEvidenceSHA256: fixture.scope.ManifestSHA256, OpenRosterVersion: 2},
	}
	if err := service.db.Create(&installations).Error; err != nil {
		t.Fatal(err)
	}
	return fixture
}

func assertInstallationViewCounts(t *testing.T, summary CurrentSummary, view string, total, active, historical, unregistered, scopes int) {
	t.Helper()
	if summary.ContractVersion != "disk-installation-view-v1" || summary.View != view || summary.Total != total || summary.ActiveCount != active || summary.HistoricalCount != historical || summary.UnregisteredCount != unregistered || summary.RegisteredScopeCount != scopes {
		t.Fatalf("installation view denominator mismatch: view=%s total=%d active=%d historical=%d unregistered=%d scopes=%d", summary.View, summary.Total, summary.ActiveCount, summary.HistoricalCount, summary.UnregisteredCount, summary.RegisteredScopeCount)
	}
}

func TestDiskInstallationViewDoesNotEnrolLegacySnapshots(t *testing.T) {
	_, service := newDiskServiceTestDB(t)
	service.now = func() time.Time { return cutoff }
	report := installationViewReport(t, "legacy", cutoff.Add(-10*time.Minute), false)
	if _, err := service.PersistReport(report, ""); err != nil {
		t.Fatal(err)
	}
	result, err := service.ListDisks(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 0 || len(result.Data) != 0 {
		t.Fatal("unregistered snapshot became an active installation")
	}
	assertInstallationViewCounts(t, result.Summary, "active", 0, 0, 0, 1, 0)
	summary, err := service.SummaryForView("unregistered")
	if err != nil {
		t.Fatal(err)
	}
	assertInstallationViewCounts(t, summary, "unregistered", 1, 0, 0, 1, 0)
	result, err = service.ListDisks(ListOptions{View: "unregistered"})
	if err != nil || result.Total != 1 || len(result.Data) != 1 || result.Data[0].Installation.State != "unregistered" || result.Data[0].Installation.InstallationID != "" {
		t.Fatalf("unregistered evidence missing or fabricated metadata: %v", err)
	}
}

func TestDiskInstallationViewsKeepUnknownAndStaleActiveDenominator(t *testing.T) {
	fixture := newInstallationViewFixture(t)
	for _, view := range []string{"active", "historical", "unregistered", "all"} {
		result, err := fixture.service.ListDisks(ListOptions{View: view})
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if view == "all" {
			want = 3
		}
		if result.Total != want || len(result.Data) != want {
			t.Fatalf("incorrect %s view size: total=%d rows=%d", view, result.Total, len(result.Data))
		}
		assertInstallationViewCounts(t, result.Summary, view, want, 1, 1, 1, 1)
		if view == "active" {
			row := result.Data[0]
			if row.EntityID != fixture.active.Rows[0].EntityID || row.AssessmentStatus != "insufficient_data" || row.Observation.State != "stale" || result.Summary.Unknown != 1 || result.Summary.StaleCount != 1 {
				t.Fatal("unknown or stale active installation disappeared from its denominator")
			}
		}
		if view != "all" && result.Data[0].Installation.State != view {
			t.Fatalf("wrong installation classification for %s", view)
		}
	}
	// An old report remains replayable but cannot reverse installation membership.
	if _, err := fixture.service.PersistReport(fixture.old, ""); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.ListDisks(ListOptions{})
	if err != nil || result.Total != 1 || result.Data[0].EntityID != fixture.active.Rows[0].EntityID {
		t.Fatalf("historical report reactivated an installation: %v", err)
	}
}

func TestDiskInstallationAllViewDeduplicatesInstallationSegments(t *testing.T) {
	fixture := newInstallationViewFixture(t)
	closedAt, version := fixture.opened, uint64(1)
	segment := api.DiskInstallation{
		InstallationID: "synthetic-earlier-active-entity-segment", ScopeID: fixture.scope.ScopeID,
		EntityID: fixture.active.Rows[0].EntityID, State: "superseded", OpenedAt: fixture.opened.Add(-time.Hour), ClosedAt: &closedAt,
		OpenEvidenceSHA256: Digest([]byte("synthetic-earlier-open")), CloseEvidenceSHA256: Digest([]byte("synthetic-earlier-close")),
		CloseReason: "explicit_replacement", OpenRosterVersion: 0, CloseRosterVersion: &version,
	}
	if err := fixture.service.db.Create(&segment).Error; err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.ListDisks(ListOptions{View: "all"})
	if err != nil || result.Total != 3 || len(result.Data) != 3 {
		t.Fatalf("all view duplicated an entity with several installation segments: %v", err)
	}
	assertInstallationViewCounts(t, result.Summary, "all", 3, 1, 1, 1, 1)
	seen := map[string]bool{}
	for _, row := range result.Data {
		if seen[row.EntityID] {
			t.Fatal("all view emitted duplicate latest entity")
		}
		seen[row.EntityID] = true
		if row.EntityID == fixture.active.Rows[0].EntityID && (row.Installation.State != "active" || row.Installation.InstallationID != "synthetic-active-installation") {
			t.Fatal("a closed segment hid the active installation")
		}
	}
}

func TestDiskInstallationViewFilteringAndPaginationPreserveDenominators(t *testing.T) {
	fixture := newInstallationViewFixture(t)
	result, err := fixture.service.ListDisks(ListOptions{View: "all", Order: "observed", Limit: 1, Offset: 1})
	if err != nil || result.Total != 3 || len(result.Data) != 1 || result.Data[0].EntityID != fixture.active.Rows[0].EntityID {
		t.Fatalf("pagination lost installation view total or stable ordering: %v", err)
	}
	assertInstallationViewCounts(t, result.Summary, "all", 3, 1, 1, 1, 1)
	result, err = fixture.service.ListDisks(ListOptions{View: "all", Level: "unknown", Limit: 1})
	if err != nil || result.Total != 1 || len(result.Data) != 1 || result.Data[0].EntityID != fixture.active.Rows[0].EntityID {
		t.Fatalf("level filter did not retain unknown active disk: %v", err)
	}
	assertInstallationViewCounts(t, result.Summary, "all", 3, 1, 1, 1, 1)
	result, err = fixture.service.ListDisks(ListOptions{View: "historical", Query: "installation-view-old", Protocol: "NVMe", MediaType: "SSD", Offset: 20})
	if err != nil || result.Total != 1 || len(result.Data) != 0 || result.Summary.Total != 1 {
		t.Fatalf("filtered historical total was derived from the returned page: %v", err)
	}
}

func TestDiskInstallationMetadataRemainsOnHistoricalDetailAndHistory(t *testing.T) {
	fixture := newInstallationViewFixture(t)
	for _, scenario := range []struct {
		report                Report
		state, installationID string
		closed                bool
	}{
		{fixture.old, "historical", "synthetic-old-installation", true},
		{fixture.active, "active", "synthetic-active-installation", false},
		{fixture.unregistered, "unregistered", "", false},
	} {
		entity := scenario.report.Rows[0].EntityID
		detail, err := fixture.service.Detail(entity)
		if err != nil {
			t.Fatal(err)
		}
		history, err := fixture.service.History(entity, 10, 0)
		if err != nil || len(history.Rows) != 1 {
			t.Fatalf("installation history no longer accessible: %v", err)
		}
		for _, row := range []SnapshotView{detail.SnapshotView, history.Rows[0]} {
			membership := row.Installation
			if membership.State != scenario.state || membership.InstallationID != scenario.installationID {
				t.Fatalf("detail/history lost %s installation classification", scenario.state)
			}
			if scenario.state == "unregistered" {
				if membership.ScopeID != "" || membership.RosterVersion != "" || membership.OpenedAt != nil || membership.ClosedAt != nil {
					t.Fatal("unregistered detail fabricated registry metadata")
				}
			} else if membership.ScopeID != fixture.scope.ScopeID || membership.RosterVersion != "2" || membership.OpenedAt == nil || (membership.ClosedAt != nil) != scenario.closed {
				t.Fatal("registered detail/history lost exact registry version or lifetime")
			}
		}
	}
}

func installationViewDBFingerprint(t *testing.T, service *Service) string {
	t.Helper()
	// Read database columns, including evidence and uniqueness fields omitted
	// from the public JSON models. Only the fingerprint is ever returned.
	state := []any{}
	for _, query := range []struct {
		order string
		model any
	}{
		{"id", &api.InfrastructureAsset{}}, {"scope_id", &api.DiskObservationScope{}}, {"installation_id", &api.DiskInstallation{}},
		{"id", &api.DiskInstallationReconcile{}}, {"id", &api.PhysicalDiskSnapshot{}}, {"id", &api.DiskHealthEpisode{}},
	} {
		var rows []map[string]any
		if err := service.db.Model(query.model).Order(query.order).Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		state = append(state, rows)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return Digest(encoded)
}

func TestDiskInstallationGETViewValidationAndReadOnlyDatabase(t *testing.T) {
	fixture := newInstallationViewFixture(t)
	handler := NewHandler(fixture.service)
	before := installationViewDBFingerprint(t, fixture.service)
	for _, query := range []string{"?view=invalid", "?view=", "?view=active&view=all", "?view=active&view=active"} {
		recorder := httptest.NewRecorder()
		handler.HandleDisks(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/hardware/disks"+query, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid/repeated installation view accepted: query=%s status=%d", query, recorder.Code)
		}
	}
	for _, view := range []string{"", "?view=active", "?view=historical", "?view=unregistered", "?view=all&limit=1&offset=1"} {
		for i := 0; i < 2; i++ {
			recorder := httptest.NewRecorder()
			handler.HandleDisks(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/hardware/disks"+view, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("GET installation view failed: %s status=%d", view, recorder.Code)
			}
		}
	}
	entity := fixture.old.Rows[0].EntityID
	for _, path := range []string{"/api/v1/hardware/disks/" + entity, "/api/v1/hardware/disks/" + entity + "/history"} {
		recorder := httptest.NewRecorder()
		handler.HandleDiskSubresource(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("historical GET failed: status=%d", recorder.Code)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, path := range []string{"/api/v1/hardware/disks", "/api/v1/hardware/disks/" + entity, "/api/v1/hardware/disks/" + entity + "/history"} {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(method, path, nil)
			if path == "/api/v1/hardware/disks" {
				handler.HandleDisks(recorder, request)
			} else {
				handler.HandleDiskSubresource(recorder, request)
			}
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Fatalf("write method accepted on read-only disk path: method=%s status=%d", method, recorder.Code)
			}
		}
	}
	if after := installationViewDBFingerprint(t, fixture.service); after != before {
		t.Fatal("GET/query validation changed inventory, registry, audit, snapshots or episodes")
	}
}

func TestDiskInstallationRegistryGETIsReadOnlyAndVersioned(t *testing.T) {
	fixture := newInstallationViewFixture(t)
	handler := NewHandler(fixture.service)
	before := installationViewDBFingerprint(t, fixture.service)
	for _, query := range []string{"", "?scope_id=" + fixture.scope.ScopeID} {
		recorder := httptest.NewRecorder()
		handler.HandleInstallationRegistry(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/hardware/disk-installations"+query, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("installation registry GET failed: %d", recorder.Code)
		}
		var response struct {
			ContractVersion string `json:"contract_version"`
			Data            []struct {
				ScopeID           string `json:"scope_id"`
				OpenRosterVersion string `json:"open_roster_version"`
			} `json:"data"`
			Scopes []struct {
				ScopeID        string `json:"scope_id"`
				AssetKey       string `json:"asset_key"`
				RosterVersion  string `json:"roster_version"`
				ManifestSHA256 string `json:"manifest_sha256"`
			} `json:"scopes"`
			Meta struct {
				Total int `json:"total"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("registry versions must be JSON decimal strings: %v", err)
		}
		if response.ContractVersion != "disk-installation-view-v1" || response.Meta.Total != 2 || len(response.Data) != 2 || len(response.Scopes) != 1 {
			t.Fatal("registry GET omitted installation segments, scope or contract")
		}
		scope := response.Scopes[0]
		if scope.ScopeID != fixture.scope.ScopeID || scope.AssetKey != fixture.scope.AssetKey || scope.RosterVersion != "2" || scope.ManifestSHA256 != fixture.scope.ManifestSHA256 {
			t.Fatal("registry scope lost bound asset, exact version or evidence SHA")
		}
		for _, segment := range response.Data {
			if segment.ScopeID != fixture.scope.ScopeID || segment.OpenRosterVersion == "" {
				t.Fatal("registry segment lost scope or exact opening version")
			}
		}
	}
	for _, query := range []string{"?scope_id=", "?scope_id=a&scope_id=b", "?scope_id=a&scope_id=a"} {
		recorder := httptest.NewRecorder()
		handler.HandleInstallationRegistry(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/hardware/disk-installations"+query, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid/repeated registry scope accepted: query=%s status=%d", query, recorder.Code)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		handler.HandleInstallationRegistry(recorder, httptest.NewRequest(method, "/api/v1/hardware/disk-installations", nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("registry accepted mutation method: %s status=%d", method, recorder.Code)
		}
	}
	if after := installationViewDBFingerprint(t, fixture.service); after != before {
		t.Fatal("registry GET changed inventory, installations, audit, snapshots or episodes")
	}
}
