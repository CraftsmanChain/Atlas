package gpurisk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"atlas/pkg/api"
)

func TestCoreSourcePlanAvailabilityAndIdentity(t *testing.T) {
	for _, offset := range []time.Duration{-time.Microsecond, 0, time.Microsecond} {
		t.Run(offset.String(), func(t *testing.T) {
			db, q, _ := outcomeDB(t)
			v := sourceInput(q)
			row := sourceRow(v, q.Cutoff.Add(offset), 1)
			if e := db.Create(&row).Error; e != nil {
				t.Fatal(e)
			}
			at := time.Now().UTC()
			p, e := BuildCoreSourcePlan(t.Context(), db, q.ID, at)
			if e != nil {
				t.Fatal(e)
			}
			want := 2
			if offset < 0 {
				want = 1
			}
			if p.Counts["missing_context:gpu_model"] != want || p.Counts["N"] != 2 {
				t.Fatal(p.Counts)
			}
			again, e := BuildCoreSourcePlan(t.Context(), db, q.ID, at)
			if e != nil || p.SHA != again.SHA {
				t.Fatal("unstable", e)
			}
			if offset < 0 {
				v.State = "withdrawn"
				v.Context = nil
				withdrawn := sourceRow(v, q.Cutoff.Add(-time.Nanosecond*2000), 2)
				if e := db.Create(&withdrawn).Error; e != nil {
					t.Fatal(e)
				}
				changed, e := BuildCoreSourcePlan(t.Context(), db, q.ID, at)
				if e != nil || changed.SHA == p.SHA || changed.Counts["missing_context:gpu_model"] != 2 {
					t.Fatal("withdrawal", e, changed.Counts)
				}
			}
		})
	}
	for _, change := range []string{"generation", "entity"} {
		t.Run(change, func(t *testing.T) {
			db, q, _ := outcomeDB(t)
			v := sourceInput(q)
			if change == "generation" {
				v.Generation++
			} else {
				v.EntityRef = "other"
			}
			row := sourceRow(v, q.Cutoff.Add(-time.Minute), 1)
			db.Create(&row)
			p, e := BuildCoreSourcePlan(t.Context(), db, q.ID, time.Now())
			if e != nil || p.Counts["missing_context:gpu_model"] != 2 {
				t.Fatal(e, p.Counts)
			}
		})
	}
}
func TestCoreSourcePlanEmptyTemplatesAndEpisodes(t *testing.T) {
	db, q, _ := outcomeDB(t)
	at := time.Now().UTC()
	p, e := BuildCoreSourcePlan(t.Context(), db, q.ID, at)
	if e != nil {
		t.Fatal(e)
	}
	if p.Counts["episode_state_unknown_without_known_episode"] != 2 || len(p.Episodes) != 0 {
		t.Fatal(p)
	}
	raw, _ := json.Marshal(p.Members[0].Template)
	if strings.Contains(string(raw), "available_at") || strings.Contains(string(raw), "unknown") {
		t.Fatal(string(raw))
	}
	var before int64
	db.Model(&api.GPURiskEvidenceRevision{}).Count(&before)
	_, r := seedReview(t, db, q, "core-source-episode", -time.Hour*24)
	db.Model(&r).Updates(map[string]any{"created_at": q.Cutoff.Add(-time.Hour), "reviewed_at": q.Cutoff.Add(-time.Hour)})
	identity := EvidenceInput{Kind: "identity", Key: "hist-id", EntityRef: "serial:sn-1", FeedbackID: r.FeedbackRequestID, ReviewSHA: r.ReviewSHA256, From: q.Cutoff.Add(-time.Hour * 25), Through: q.Cutoff.Add(-time.Hour * 23)}
	row := sourceRow(identity, q.Cutoff.Add(-time.Hour), 1)
	row.SHA = strings.Repeat("d", 64)
	db.Create(&row)
	p, e = BuildCoreSourcePlan(t.Context(), db, q.ID, at)
	if e != nil || len(p.Episodes) != 1 || p.Counts["independent_episodes"] != 1 {
		t.Fatal(e, p)
	}
	if len(p.Episodes[0].Candidates) != 1 || p.Episodes[0].Template["node_id"] != uint(1) || p.Episodes[0].Template["generation"] != 0 {
		t.Fatal("lost known member identity", p.Episodes[0])
	}
	duplicate := row
	duplicate.ID = 0
	duplicate.Key = "second-id"
	identity.Key = duplicate.Key
	value, _ := json.Marshal(identity)
	duplicate.Data = string(value)
	duplicate.SHA = strings.Repeat("e", 64)
	db.Create(&duplicate)
	p, e = BuildCoreSourcePlan(t.Context(), db, q.ID, at)
	if e != nil || len(p.Episodes) != 1 {
		t.Fatal("duplicate episode", e, p)
	}
	var after int64
	db.Model(&api.GPURiskEvidenceRevision{}).Count(&after)
	if after != before+2 {
		t.Fatal("unexpected write", after)
	}
	for _, bad := range []time.Time{time.Time{}, q.Cutoff, time.Now().Add(time.Hour)} {
		if _, e := BuildCoreSourcePlan(t.Context(), db, q.ID, bad); e == nil {
			t.Fatal("accepted", bad)
		}
	}
}

func TestCoreSourcePlanGlobalRevisionBeforeEntityIndex(t *testing.T) {
	db, q, _ := outcomeDB(t)
	v := sourceInput(q)
	row := sourceRow(v, q.Cutoff.Add(-time.Hour), 1)
	if e := db.Create(&row).Error; e != nil {
		t.Fatal(e)
	}
	moved := v
	moved.EntityRef = "serial:sn-2"
	moved.NodeID = 2
	next := sourceRow(moved, q.Cutoff.Add(-time.Minute), 2)
	if e := db.Create(&next).Error; e != nil {
		t.Fatal(e)
	}
	p, e := BuildCoreSourcePlan(t.Context(), db, q.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Members[0].MissingContext) != 7 || len(p.Members[1].MissingContext) != 0 {
		t.Fatal("stale entity inherited superseded revision", p.Members)
	}
}
func TestCoreSourcePlanRejectsPersistedMemberTampering(t *testing.T) {
	db, q, _ := outcomeDB(t)
	if e := db.Model(&api.GPURiskMember{}).Where("query_id = ? AND node_id = ?", q.ID, 1).Update("entity_ref", "tampered").Error; e != nil {
		t.Fatal(e)
	}
	if _, e := BuildCoreSourcePlan(t.Context(), db, q.ID, time.Now()); e == nil {
		t.Fatal("accepted mutated frozen member")
	}
}
