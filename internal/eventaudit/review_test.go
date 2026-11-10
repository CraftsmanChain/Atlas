package eventaudit

import (
	"atlas/pkg/api"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewPreparationStableLinksAndNoConfirmation(t *testing.T) {
	db, _ := fixture(t)
	f := api.HardwareFaultFeedbackRequest{ID: 7, RequestKey: "old", NodeIP: "10.1.1.1", GPUUUID: "GPU-A", Description: "PRIVATE-RAW", TrainingEligible: true}
	if e := db.Create(&f).Error; e != nil {
		t.Fatal(e)
	}
	for _, r := range []api.HardwareFaultFeedbackReview{{ID: 2, FeedbackRequestID: 7, Revision: 1, Decision: "confirmed_hardware", ReviewSHA256: "a"}, {ID: 3, FeedbackRequestID: 7, Revision: 2, Decision: "rejected", ReviewSHA256: "b"}} {
		if e := db.Create(&r).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e := db.Create(&api.HardwareFaultFeedbackRequest{ID: 8, RequestKey: "node", NodeIP: "10.1.1.1", TargetScope: "node"}).Error; e != nil {
		t.Fatal(e)
	}
	a, e := PrepareReviews(context.Background(), db, "cluster-a", []uint{1})
	if e != nil {
		t.Fatal(e)
	}
	b, e := PrepareReviews(context.Background(), db, "cluster-a", []uint{1})
	if e != nil || a.SHA != b.SHA {
		t.Fatal("unstable plan")
	}
	v := a.Items[0]
	if v.TrainingEligible || v.Stage != "review_pending" || len(v.LatestReviewIDs) != 1 || v.LatestReviewIDs[0] != 3 || len(v.NodeFeedbackIDs) != 1 {
		t.Fatal(v)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "GPU-A") || strings.Contains(string(raw), "10.1.1.1") {
		t.Fatal("raw data exported")
	}
	oldKey := v.CandidateKey
	if e = db.Model(&f).Update("description", "PRIVATE-CHANGED").Error; e != nil {
		t.Fatal(e)
	}
	c, e := PrepareReviews(context.Background(), db, "cluster-a", []uint{1})
	if e != nil || c.Items[0].CandidateKey != oldKey || c.SHA == a.SHA {
		t.Fatal("source update must preserve key and change evidence")
	}
	var count int64
	db.Model(&api.HardwareFaultFeedbackReview{}).Count(&count)
	if count != 2 {
		t.Fatal("review written")
	}
	db.Model(&api.HardwareFaultFeedbackRequest{}).Count(&count)
	if count != 2 {
		t.Fatal("feedback written")
	}
	for _, ids := range [][]uint{{}, {1, 1}, {0}, {1, 999}} {
		if _, e = PrepareReviews(context.Background(), db, "cluster-a", ids); e == nil {
			t.Fatal("ambiguous/partial plan allowed", ids)
		}
	}
}
