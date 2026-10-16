package diskhealth

import (
	"encoding/json"
	"testing"
	"time"

	"atlas/pkg/api"
)

func diskEpisodeReport(t *testing.T, at time.Time, media string) Report {
	t.Helper()
	report := diskServiceReport(t, at, media)
	for i := range report.Rows[0].Hits {
		report.Rows[0].Hits[i].EvidenceID = "evidence-" + at.Format(time.RFC3339)
	}
	return report
}

func persistedEpisode(t *testing.T, service *Service, entityID, rule string) api.DiskHealthEpisode {
	t.Helper()
	var episode api.DiskHealthEpisode
	if err := service.db.Where("entity_id = ? AND rule_code = ?", entityID, rule).First(&episode).Error; err != nil {
		t.Fatal(err)
	}
	return episode
}

func TestDiskEpisodeHistoricalReplayIsOrderSafe(t *testing.T) {
	for name, order := range map[string][]int{
		"A_B_A":       {0, 1, 0},
		"B_A":         {1, 0},
		"A_B_A_B_A_B": {0, 1, 0, 1, 0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, service := newDiskServiceTestDB(t)
			first := diskEpisodeReport(t, cutoff.Add(-2*time.Hour), "1")
			first.Rows[0].Hits[0].Level = "critical"
			last := diskEpisodeReport(t, cutoff.Add(-time.Hour), "2")
			last.Rows[0].Device = "/dev/nvme9"
			reports := []Report{first, last}
			for _, index := range order {
				if _, err := service.PersistReport(reports[index], ""); err != nil {
					t.Fatal(err)
				}
			}
			episode := persistedEpisode(t, service, first.Rows[0].EntityID, "historical_media_errors")
			if episode.OccurrenceCount != 2 || !episode.FirstObservedAt.Equal(first.Rows[0].ObservedAt) || !episode.LastObservedAt.Equal(last.Rows[0].ObservedAt) {
				t.Fatalf("replay changed independent occurrences or time range: %+v", episode)
			}
			if episode.Severity != "critical" || episode.EvidenceID != last.Rows[0].Hits[0].EvidenceID || episode.Device != last.Rows[0].Device {
				t.Fatalf("severity or latest source regressed: %+v", episode)
			}
			var latest api.PhysicalDiskSnapshot
			if err := service.db.Where("snapshot_key = ?", snapshotKey(last.Rows[0])).First(&latest).Error; err != nil {
				t.Fatal(err)
			}
			if episode.LatestSnapshotID != latest.ID || !latest.Current {
				t.Fatalf("latest hit/current moved to an older observation: episode=%+v latest=%+v", episode, latest)
			}
			var snapshots int64
			service.db.Model(&api.PhysicalDiskSnapshot{}).Count(&snapshots)
			if snapshots != 2 {
				t.Fatalf("snapshot replay duplicated observations: %d", snapshots)
			}
			result, err := service.PersistReport(first, "")
			if err != nil || result.EpisodesUpserted != 0 || result.CurrentPromoted != 0 {
				t.Fatalf("unchanged historical replay was not a no-op: %+v %v", result, err)
			}
		})
	}
}

func TestDiskEpisodeDeduplicatesPhysicalObservationAcrossRulesAndVersions(t *testing.T) {
	_, service := newDiskServiceTestDB(t)
	base := diskEpisodeReport(t, cutoff.Add(-time.Hour), "1")
	base.Rows[0].Hits = append(base.Rows[0].Hits, base.Rows[0].Hits[0], Hit{Rule: "path_review", Level: "attention", EvidenceID: "path"})
	reviewed := diskEpisodeReport(t, base.Rows[0].ObservedAt, "1")
	reviewed.Rows[0].RuleVersion += "-reviewed"
	reviewed.Rows[0].Hits = append(reviewed.Rows[0].Hits, Hit{Rule: "path_review", Level: "warning", EvidenceID: "path-reviewed"}, Hit{Rule: "new_review", Level: "attention", EvidenceID: "new"})
	laterAvailable := diskEpisodeReport(t, base.Rows[0].ObservedAt, "1")
	laterAvailable.Rows[0].AvailableAt = laterAvailable.Rows[0].AvailableAt.Add(time.Minute)
	laterAvailable.Rows[0].Hits[0].EvidenceID = "later-available"
	for _, report := range []Report{base, reviewed, laterAvailable, base, reviewed} {
		if _, err := service.PersistReport(report, ""); err != nil {
			t.Fatal(err)
		}
	}
	var episodes []api.DiskHealthEpisode
	if err := service.db.Order("rule_code ASC").Find(&episodes).Error; err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 3 {
		t.Fatalf("rules should remain independent: %+v", episodes)
	}
	for _, episode := range episodes {
		if episode.OccurrenceCount != 1 || !episode.FirstObservedAt.Equal(base.Rows[0].ObservedAt) || !episode.LastObservedAt.Equal(base.Rows[0].ObservedAt) {
			t.Fatalf("one physical observation was counted repeatedly: %+v", episode)
		}
	}
	path := persistedEpisode(t, service, base.Rows[0].EntityID, "path_review")
	if path.Severity != "warning" {
		t.Fatalf("rule re-evaluation lost stronger severity: %+v", path)
	}
	media := persistedEpisode(t, service, base.Rows[0].EntityID, "historical_media_errors")
	if media.EvidenceID != "later-available" {
		t.Fatalf("latest source availability was not selected: %+v", media)
	}
}

func TestDiskEpisodeSameTimeSourceUsesStableSnapshotKey(t *testing.T) {
	var selectedKey string
	for _, reverse := range []bool{false, true} {
		_, service := newDiskServiceTestDB(t)
		first := diskEpisodeReport(t, cutoff.Add(-time.Hour), "1")
		first.Rows[0].RuleVersion = "review-a"
		first.Rows[0].Hits[0].EvidenceID = "source-a"
		second := diskEpisodeReport(t, first.Rows[0].ObservedAt, "1")
		second.Rows[0].RuleVersion = "review-b"
		second.Rows[0].Hits[0].EvidenceID = "source-b"
		reports := []Report{first, second}
		if reverse {
			reports[0], reports[1] = reports[1], reports[0]
		}
		for _, report := range reports {
			if _, err := service.PersistReport(report, ""); err != nil {
				t.Fatal(err)
			}
		}
		episode := persistedEpisode(t, service, first.Rows[0].EntityID, "historical_media_errors")
		var source api.PhysicalDiskSnapshot
		if err := service.db.First(&source, episode.LatestSnapshotID).Error; err != nil {
			t.Fatal(err)
		}
		want := snapshotKey(first.Rows[0])
		if other := snapshotKey(second.Rows[0]); other > want {
			want = other
		}
		if source.SnapshotKey != want || (selectedKey != "" && source.SnapshotKey != selectedKey) {
			t.Fatalf("same-time source depends on insertion order: key=%s want=%s previous=%s", source.SnapshotKey, want, selectedKey)
		}
		selectedKey = source.SnapshotKey
	}
}

func TestDiskEpisodeUnchangedValueAtLaterObservationStillCounts(t *testing.T) {
	_, service := newDiskServiceTestDB(t)
	first := diskEpisodeReport(t, cutoff.Add(-2*time.Hour), "1")
	last := diskEpisodeReport(t, cutoff.Add(-time.Hour), "1")
	if first.Rows[0].InputSHA256 != last.Rows[0].InputSHA256 {
		t.Fatal("fixture must preserve identical payload across observations")
	}
	for _, report := range []Report{first, last, first} {
		if _, err := service.PersistReport(report, ""); err != nil {
			t.Fatal(err)
		}
	}
	episode := persistedEpisode(t, service, first.Rows[0].EntityID, "historical_media_errors")
	if episode.OccurrenceCount != 2 || !episode.LastObservedAt.Equal(last.Rows[0].ObservedAt) {
		t.Fatalf("deduplication collapsed distinct observation times: %+v", episode)
	}
}

func TestDiskEpisodeSourceOrderUsesInstantsAcrossTimezones(t *testing.T) {
	_, service := newDiskServiceTestDB(t)
	first := diskEpisodeReport(t, cutoff.Add(-2*time.Hour).In(time.FixedZone("UTC+02", 2*60*60)), "1")
	last := diskEpisodeReport(t, cutoff.Add(-time.Hour), "2")
	for _, report := range []Report{last, first} {
		if _, err := service.PersistReport(report, ""); err != nil {
			t.Fatal(err)
		}
	}
	episode := persistedEpisode(t, service, first.Rows[0].EntityID, "historical_media_errors")
	if episode.OccurrenceCount != 2 || !episode.FirstObservedAt.Equal(first.Rows[0].ObservedAt) ||
		!episode.LastObservedAt.Equal(last.Rows[0].ObservedAt) || episode.EvidenceID != last.Rows[0].Hits[0].EvidenceID {
		t.Fatalf("timezone text order changed chronological source selection: %+v", episode)
	}
}

func TestDiskEpisodeDuplicateNoHitImportRepairsSavedAggregate(t *testing.T) {
	_, service := newDiskServiceTestDB(t)
	first := diskEpisodeReport(t, cutoff.Add(-3*time.Hour), "1")
	last := diskEpisodeReport(t, cutoff.Add(-2*time.Hour), "2")
	clear := diskEpisodeReport(t, cutoff.Add(-time.Hour), "0")
	for _, report := range []Report{first, last, clear} {
		if _, err := service.PersistReport(report, ""); err != nil {
			t.Fatal(err)
		}
	}
	episode := persistedEpisode(t, service, first.Rows[0].EntityID, "historical_media_errors")
	recovered := cutoff.Add(-30 * time.Minute)
	if err := service.db.Model(&api.DiskHealthEpisode{}).Where("id = ?", episode.ID).Updates(map[string]any{
		"occurrence_count": 99, "first_observed_at": last.Rows[0].ObservedAt, "last_observed_at": first.Rows[0].ObservedAt,
		"latest_snapshot_id": 0, "evidence_id": "corrupted", "severity": "critical", "state": "dismissed", "recovered_at": recovered,
	}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := service.PersistReport(clear, "")
	if err != nil || result.SnapshotsCreated != 0 || result.SnapshotsDuplicate != 1 || result.CurrentPromoted != 0 || result.EpisodesUpserted != 1 {
		t.Fatalf("duplicate no-hit report did not repair saved aggregation: %+v %v", result, err)
	}
	fixed := persistedEpisode(t, service, first.Rows[0].EntityID, "historical_media_errors")
	if fixed.OccurrenceCount != 2 || !fixed.FirstObservedAt.Equal(first.Rows[0].ObservedAt) || !fixed.LastObservedAt.Equal(last.Rows[0].ObservedAt) || fixed.EvidenceID != last.Rows[0].Hits[0].EvidenceID {
		t.Fatalf("saved aggregation not rebuilt from snapshot evidence: %+v", fixed)
	}
	if fixed.State != "dismissed" || fixed.RecoveredAt == nil || !fixed.RecoveredAt.Equal(recovered) || fixed.Severity != "critical" {
		t.Fatalf("replay changed review state or weakened severity: %+v", fixed)
	}
	if result, err := service.PersistReport(clear, ""); err != nil || result.EpisodesUpserted != 0 {
		t.Fatalf("repaired aggregation was updated again: %+v %v", result, err)
	}
}

func TestDiskEpisodeRebuildFailureRollsBackReport(t *testing.T) {
	_, service := newDiskServiceTestDB(t)
	first := diskEpisodeReport(t, cutoff.Add(-2*time.Hour), "1")
	if _, err := service.PersistReport(first, ""); err != nil {
		t.Fatal(err)
	}
	if err := service.db.Model(&api.PhysicalDiskSnapshot{}).Where("snapshot_key = ?", snapshotKey(first.Rows[0])).Update("hits_json", "invalid-json").Error; err != nil {
		t.Fatal(err)
	}
	last := diskEpisodeReport(t, cutoff.Add(-time.Hour), "2")
	if _, err := service.PersistReport(last, ""); err == nil {
		t.Fatal("invalid persisted evidence should fail the report transaction")
	}
	var snapshots []api.PhysicalDiskSnapshot
	if err := service.db.Find(&snapshots).Error; err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || !snapshots[0].Current {
		t.Fatalf("failed rebuild committed snapshots or current promotion: %+v", snapshots)
	}
	var hits []Hit
	if err := json.Unmarshal([]byte(snapshots[0].HitsJSON), &hits); err == nil {
		t.Fatal("corrupt source was silently replaced")
	}
}
