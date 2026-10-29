// Package gpurisk records immutable node ranking cohorts. It never emits
// probabilities, alerts or operational actions and has no automatic worker.
package gpurisk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"atlas/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const Contract = "gpu-node-ranking-v1"
const Target = "confirmed_hardware_failure"
const Uniform = "uniform-v1"
const HealthInverse = "health-inverse-v1"
const MaxNodes = 10000

var ErrBusy = errors.New("ranking job busy or attempt budget exhausted")
var ErrFence = errors.New("ranking job lease expired or superseded")
var ErrInput = errors.New("invalid ranking snapshot")

type Card struct {
	AssetID     uint      `json:"asset_id"`
	UUID        string    `json:"uuid"`
	State       string    `json:"state"`
	HealthID    uint      `json:"health_id"`
	RunID       uint      `json:"run_id"`
	Rule        string    `json:"rule"`
	Score       *int      `json:"score"`
	EvaluatedAt time.Time `json:"evaluated_at"`
	AvailableAt time.Time `json:"available_at"`
}
type Node struct {
	ScopeStatus    string    `json:"scope_status"`
	SourceAssetIDs []uint    `json:"source_asset_ids"`
	ID             uint      `json:"id"`
	Generation     int       `json:"generation"`
	IP             string    `json:"ip"`
	Serial         string    `json:"serial"`
	Lifecycle      string    `json:"lifecycle"`
	Expected       int       `json:"expected"`
	Observed       int       `json:"observed"`
	AvailableAt    time.Time `json:"available_at"`
	Cards          []Card    `json:"cards"`
}
type Snapshot struct {
	Contract string    `json:"contract"`
	Target   string    `json:"target"`
	Cutoff   time.Time `json:"cutoff"`
	Nodes    []Node    `json:"nodes"`
	SHA      string    `json:"sha"`
}

func digest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func seal(p Snapshot) (Snapshot, error) {
	p.SHA = ""
	sort.Slice(p.Nodes, func(i, j int) bool { return p.Nodes[i].ID < p.Nodes[j].ID })
	for i := range p.Nodes {
		sort.Slice(p.Nodes[i].Cards, func(a, b int) bool { return p.Nodes[i].Cards[a].AssetID < p.Nodes[i].Cards[b].AssetID })
	}
	sha, err := digest(p)
	p.SHA = sha
	return p, err
}

// Capture reads the current inventory in one read-only transaction. There is no
// historical cutoff argument: current identity cannot reconstruct an old scope.
func Capture(ctx context.Context, db *gorm.DB) (Snapshot, error) {
	p := Snapshot{Contract: Contract, Target: Target, Nodes: []Node{}}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var nodes []api.GPUNode
		if err := tx.Order("id").Limit(MaxNodes + 1).Find(&nodes).Error; err != nil {
			return err
		}
		if len(nodes) > MaxNodes {
			return ErrInput
		}
		// Bound inputs by the fleet budget, rather than querying raw telemetry.
		var assets []api.GPUAsset
		if err := tx.Where("current_node_identity = ?", true).Order("id").Limit(MaxNodes*16 + 1).Find(&assets).Error; err != nil {
			return err
		}
		if len(assets) > MaxNodes*16 {
			return ErrInput
		}
		var sourceAssets []api.InfrastructureAsset
		if err := tx.Where("source = ? AND entity_kind = ? AND present = ?", "asset_machine", "gpu_node", true).Order("id").Limit(MaxNodes*2 + 1).Find(&sourceAssets).Error; err != nil {
			return err
		}
		if len(sourceAssets) > MaxNodes*2 {
			return ErrInput
		}
		sourcesBySerial := map[string][]api.InfrastructureAsset{}
		for _, a := range sourceAssets {
			if a.SerialNumber != "" {
				sourcesBySerial[a.SerialNumber] = append(sourcesBySerial[a.SerialNumber], a)
			}
		}
		assetsByNode := make(map[uint][]api.GPUAsset)
		for _, a := range assets {
			assetsByNode[a.NodeID] = append(assetsByNode[a.NodeID], a)
		}
		// Cutoff is captured before reading scores; only already available rows qualify.
		p.Cutoff = time.Now().UTC().Truncate(time.Microsecond)
		for _, n := range nodes {
			node := Node{ID: n.ID, Generation: n.IdentityGeneration, IP: n.NodeIP, Serial: n.AssetSerial, Lifecycle: n.Lifecycle, Expected: n.ExpectedGPUCount, Observed: n.ObservedGPUCount, AvailableAt: n.UpdatedAt.UTC(), Cards: []Card{}}
			node.ScopeStatus = "scope_unconfirmed"
			matches := sourcesBySerial[n.AssetSerial]
			for _, a := range matches {
				node.SourceAssetIDs = append(node.SourceAssetIDs, a.ID)
			}
			if len(matches) > 1 {
				node.ScopeStatus = "source_identity_conflict"
			} else if len(matches) == 1 {
				a := matches[0]
				if a.IPAddress == n.NodeIP && !a.UpdatedAt.IsZero() && a.UpdatedAt.Before(p.Cutoff) && !a.UpdatedAt.Before(p.Cutoff.Add(-24*time.Hour)) {
					node.ScopeStatus = "not_in_use"
					if a.InUse {
						node.ScopeStatus = "in_use"
					}
				}
			}
			for _, a := range assetsByNode[n.ID] {
				if a.NodeID != n.ID || a.NodeIdentityGeneration != n.IdentityGeneration {
					continue
				}
				card := Card{AssetID: a.ID, UUID: a.CurrentUUID, State: a.State}
				// UUID plus asset plus node bind scores to this installation. A snapshot
				// from before the current asset/generation existed cannot supply a score.
				if a.CurrentUUID != "" && a.State == "active" && a.UpdatedAt.Before(p.Cutoff) {
					var h api.GPUHealthScore
					lower := p.Cutoff.Add(-24 * time.Hour)
					if a.FirstSeenAt.After(lower) {
						lower = a.FirstSeenAt
					}
					if n.IdentityChangedAt.After(lower) {
						lower = n.IdentityChangedAt
					}
					err := tx.Where("gpu_asset_id = ? AND gpu_uuid = ? AND node_ip = ? AND "+timeSQL(tx, "evaluated_at", ">=")+" AND "+timeSQL(tx, "evaluated_at", "<")+" AND "+timeSQL(tx, "created_at", "<"), a.ID, a.CurrentUUID, n.NodeIP, lower, p.Cutoff, p.Cutoff).Order(timeOrder(tx, "evaluated_at")).First(&h).Error
					if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
						return err
					}
					if err == nil {
						var run api.HealthEvaluationRun
						e := tx.Where("id = ? AND status = ? AND "+timeSQL(tx, "finished_at", "<"), h.EvaluationRunID, "success", p.Cutoff).First(&run).Error
						if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
							return e
						}
						if e == nil && run.FinishedAt != nil && run.FinishedAt.Before(p.Cutoff) && h.EvaluatedAt.Before(p.Cutoff) && !h.EvaluatedAt.Before(lower) && h.CreatedAt.Before(p.Cutoff) {
							card.HealthID = h.ID
							card.RunID = h.EvaluationRunID
							card.Rule = h.RuleVersion
							card.Score = h.Score
							card.EvaluatedAt = h.EvaluatedAt.UTC()
							card.AvailableAt = h.CreatedAt.UTC()
							if run.FinishedAt.After(card.AvailableAt) {
								card.AvailableAt = run.FinishedAt.UTC()
							}
						}
					}
				}
				node.Cards = append(node.Cards, card)
			}
			p.Nodes = append(p.Nodes, node)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return p, err
	}
	return seal(p)
}

func validate(p Snapshot, now time.Time) error {
	if p.Contract != Contract || p.Target != Target || p.Cutoff.IsZero() || p.Cutoff.After(now) || len(p.Nodes) == 0 || len(p.Nodes) > MaxNodes {
		return ErrInput
	}
	q, err := seal(p)
	if err != nil || q.SHA != p.SHA {
		return ErrInput
	}
	seen := map[uint]bool{}
	cards := map[uint]bool{}
	for _, n := range p.Nodes {
		if n.ID == 0 || seen[n.ID] || (n.ScopeStatus != "in_use" && n.ScopeStatus != "not_in_use" && n.ScopeStatus != "scope_unconfirmed" && n.ScopeStatus != "source_identity_conflict") || n.Generation < 0 || n.Expected < 0 || n.Observed < 0 || n.AvailableAt.IsZero() || !n.AvailableAt.Before(p.Cutoff) || len(n.Cards) > 64 {
			return ErrInput
		}
		seen[n.ID] = true
		for _, c := range n.Cards {
			if c.AssetID == 0 || cards[c.AssetID] {
				return ErrInput
			}
			cards[c.AssetID] = true
			if c.Score != nil && (*c.Score < 0 || *c.Score > 100 || c.HealthID == 0 || c.RunID == 0 || c.Rule == "" || c.UUID == "" || c.State != "active" || c.AvailableAt.IsZero() || !c.AvailableAt.Before(p.Cutoff) || c.EvaluatedAt.IsZero() || !c.EvaluatedAt.Before(p.Cutoff) || c.EvaluatedAt.Before(p.Cutoff.Add(-24*time.Hour))) {
				return ErrInput
			}
		}
	}
	return nil
}

// Build produces one member and one row per policy for EVERY node, including
// blocked identities and missing signals. Rank is competition rank, never a
// fabricated distinction within ties.
func Build(p Snapshot) ([]api.GPURiskMember, []api.GPURiskScore, error) {
	if err := validate(p, time.Now()); err != nil {
		return nil, nil, err
	}
	members := []api.GPURiskMember{}
	scores := []api.GPURiskScore{}
	serials := map[string]int{}
	uuids := map[string]int{}
	for _, n := range p.Nodes {
		if n.Serial != "" {
			serials[n.Serial]++
		}
		for _, c := range n.Cards {
			if c.UUID != "" {
				uuids[c.UUID]++
			}
		}
	}
	for _, n := range p.Nodes {
		m := api.GPURiskMember{NodeID: n.ID, Generation: n.Generation, NodeIP: n.IP, ExpectedGPUs: n.Expected, ObservedGPUs: n.Observed, Status: "included", Reason: "history_unknown"}
		if n.Serial == "" {
			m.Status = "blocked"
			m.Reason = "identity_unknown"
		} else if serials[n.Serial] > 1 {
			m.Status = "blocked"
			m.Reason = "serial_conflict"
		} else {
			m.EntityRef = "serial:" + n.Serial
		}
		if m.Status == "included" && n.ScopeStatus != "in_use" {
			m.Status = "blocked"
			m.Reason = n.ScopeStatus
			if n.ScopeStatus == "not_in_use" {
				m.Status = "excluded"
			}
		}
		if n.Lifecycle == "discovered" {
			m.Status = "blocked"
			m.Reason = "lifecycle_unconfirmed"
		}
		if n.Lifecycle != "active" && n.Lifecycle != "discovered" {
			m.Status = "excluded"
			m.Reason = "lifecycle_" + n.Lifecycle
		}
		members = append(members, m)
		u := api.GPURiskScore{NodeID: n.ID, Generation: n.Generation, Policy: Uniform, SignalStatus: m.Status, Evidence: api.StringList{m.Reason}}
		h := api.GPURiskScore{NodeID: n.ID, Generation: n.Generation, Policy: HealthInverse, SignalStatus: "no_signal", Evidence: api.StringList{}}
		valid := 0
		max := 0.0
		for _, c := range n.Cards {
			if c.Score == nil || uuids[c.UUID] != 1 {
				continue
			}
			valid++
			v := 100 - float64(*c.Score)
			if v > max {
				max = v
			}
			h.Evidence = append(h.Evidence, fmt.Sprintf("health:%d/run:%d/rule:%s", c.HealthID, c.RunID, c.Rule))
		}
		denom := n.Expected
		if len(n.Cards) > denom {
			denom = len(n.Cards)
		}
		if n.Observed > denom {
			denom = n.Observed
		}
		if denom > 0 {
			h.Coverage = float64(valid) / float64(denom)
		}
		if m.Status == "included" {
			zero := 0.0
			u.RawScore = &zero
			u.Coverage = 1
			if valid > 0 {
				h.RawScore = &max
				h.SignalStatus = "complete"
				if valid < denom {
					h.SignalStatus = "partial"
				}
			}
		} else {
			h.SignalStatus = m.Status
			h.Evidence = api.StringList{m.Reason}
		}
		scores = append(scores, u, h)
	}
	for _, policy := range []string{Uniform, HealthInverse} {
		idx := []int{}
		for i, s := range scores {
			if s.Policy == policy && s.RawScore != nil && s.SignalStatus != "partial" {
				idx = append(idx, i)
			}
		}
		sort.Slice(idx, func(i, j int) bool {
			a, b := scores[idx[i]], scores[idx[j]]
			if *a.RawScore != *b.RawScore {
				return *a.RawScore > *b.RawScore
			}
			return a.NodeID < b.NodeID
		})
		for start := 0; start < len(idx); {
			end := start + 1
			for end < len(idx) && *scores[idx[end]].RawScore == *scores[idx[start]].RawScore {
				end++
			}
			for _, i := range idx[start:end] {
				scores[i].Rank = start + 1
				scores[i].TieCount = end - start
			}
			start = end
		}
		for i := range scores {
			if scores[i].Policy == policy {
				scores[i].EligibleCount = len(idx)
				scores[i].TopFivePercentK = int(math.Ceil(float64(len(idx)) * .05))
			}
		}
	}
	return members, scores, nil
}

func Models() []any {
	return []any{&api.GPURiskModel{}, &api.GPURiskModelBatch{}, &api.GPURiskModelScore{}, &api.GPURiskEvidenceRevision{}, &api.GPURiskOutcomeBatch{}, &api.GPURiskNodeOutcome{}, &api.GPURiskScope{}, &api.GPURiskQuery{}, &api.GPURiskMember{}, &api.GPURiskScore{}, &api.GPURiskJob{}}
}
func Migrate(db *gorm.DB) error { return db.AutoMigrate(Models()...) }

type Lease struct {
	Key, Owner string
	Token      int64
}

// Claim is a single database CAS, including attempt budget and lease fencing.
func Claim(db *gorm.DB, key, owner string, now time.Time) (Lease, error) {
	job := api.GPURiskJob{Key: key, Status: "pending"}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&job).Error; err != nil {
		return Lease{}, err
	}
	r := db.Model(&api.GPURiskJob{}).Where("key = ? AND status <> ? AND attempt < ? AND (status <> ? OR lease_until <= ?)", key, "completed", 2, "running", now).Updates(map[string]any{"owner": owner, "token": gorm.Expr("token + 1"), "attempt": gorm.Expr("attempt + 1"), "lease_until": now.Add(10 * time.Minute), "status": "running", "error_code": ""})
	if r.Error != nil {
		return Lease{}, r.Error
	}
	if r.RowsAffected != 1 {
		return Lease{}, ErrBusy
	}
	if err := db.Where("key = ? AND owner = ?", key, owner).First(&job).Error; err != nil {
		return Lease{}, err
	}
	return Lease{key, owner, job.Token}, nil
}

func commit(db *gorm.DB, p Snapshot, l Lease, members []api.GPURiskMember, scores []api.GPURiskScore) (api.GPURiskQuery, error) {
	var q api.GPURiskQuery
	err := db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		r := tx.Model(&api.GPURiskJob{}).Where("key = ? AND owner = ? AND token = ? AND status = ? AND lease_until > ?", l.Key, l.Owner, l.Token, "running", now).Updates(map[string]any{"status": "completed"})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrFence
		}
		// Rebuild canonical output inside the trusted writer; reject missing rows,
		// altered scores, NaN or caller-supplied partial batches before any publication.
		em, es, err := Build(p)
		if err != nil {
			return err
		}
		actual, err := digest([]any{members, scores})
		if err != nil {
			return err
		}
		expected, _ := digest([]any{em, es})
		if actual != expected {
			return ErrInput
		}
		canonical, _ := json.Marshal(p)
		scope := api.GPURiskScope{SHA: p.SHA, Input: string(canonical)}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&scope).Error; err != nil {
			return err
		}
		q = api.GPURiskQuery{Key: l.Key, ScopeSHA: p.SHA, Contract: Contract, Target: p.Target, Cutoff: p.Cutoff, HorizonMinutes: 10080, Status: "completed", MemberCount: len(members), ResultSHA: expected, CompletedAt: &now}
		if err := tx.Create(&q).Error; err != nil {
			return err
		}
		for i := range members {
			members[i].QueryID = q.ID
		}
		for i := range scores {
			scores[i].QueryID = q.ID
		}
		if err := tx.CreateInBatches(members, 100).Error; err != nil {
			return err
		}
		return tx.CreateInBatches(scores, 100).Error
	})
	return q, err
}

// Record consumes the same frozen plan on retry; it never re-reads mutable scope.
func Record(ctx context.Context, db *gorm.DB, p Snapshot) (api.GPURiskQuery, error) {
	members, scores, err := Build(p)
	if err != nil {
		return api.GPURiskQuery{}, err
	}
	key, _ := digest([]any{p.SHA, p.Cutoff, 10080, p.Target, p.Contract})
	db = db.WithContext(ctx)
	var q api.GPURiskQuery
	err = db.Where("key = ? AND status = ?", key, "completed").First(&q).Error
	if err == nil {
		return q, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return q, err
	}
	owner, _ := digest([]any{key, time.Now().UnixNano()})
	l, err := Claim(db, key, owner, time.Now().UTC())
	if err != nil {
		if errors.Is(err, ErrBusy) && db.Where("key = ? AND status = ?", key, "completed").First(&q).Error == nil {
			return q, nil
		}
		return q, err
	}
	q, err = commit(db, p, l, members, scores)
	if err != nil {
		db.Model(&api.GPURiskJob{}).Where("key = ? AND owner = ? AND token = ? AND status = ?", l.Key, l.Owner, l.Token, "running").Updates(map[string]any{"status": "failed", "error_code": "record_failed"})
	}
	return q, err
}
