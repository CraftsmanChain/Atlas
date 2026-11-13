// Package eventaudit inventories existing evidence without producing labels or actions.
package eventaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
)

const Version = "hardware-event-checkpoint-v2"
const MaxRows = 100000

var namespacePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Entry struct {
	ID          uint   `json:"event_id"`
	Semantic    string `json:"semantic_sha256"`
	Observation string `json:"observation_sha256"`
	Dependency  string `json:"dependency_sha256"`
	Freshness   string `json:"freshness"`
}
type Snapshot struct {
	Version   string    `json:"version"`
	Namespace string    `json:"source_namespace"`
	AsOf      time.Time `json:"observed_as_of"`
	Complete  bool      `json:"complete"`
	Scope     string    `json:"scope"`
	Entries   []Entry   `json:"entries"`
	SHA       string    `json:"sha256"`
}
type Change struct {
	ID      uint     `json:"event_id"`
	Action  string   `json:"action"`
	Reasons []string `json:"reasons"`
}
type Report struct {
	Version          string         `json:"version"`
	PreviousSHA      string         `json:"previous_sha256,omitempty"`
	CurrentSHA       string         `json:"current_sha256"`
	Counts           map[string]int `json:"counts"`
	Changes          []Change       `json:"changes"`
	TrainingEligible bool           `json:"training_eligible"`
	NoActionExecuted bool           `json:"no_action_executed"`
	Limitations      []string       `json:"limitations"`
}

func Hash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func seal(s Snapshot) string { s.SHA = ""; return Hash(s) }
func (s *Snapshot) Seal()    { s.SHA = seal(*s) }
func (s Snapshot) Validate() error {
	if s.Version != Version || !namespacePattern.MatchString(s.Namespace) || s.AsOf.IsZero() || !s.Complete || s.Scope != "all_gpu_fault_events" || len(s.Entries) > MaxRows || s.SHA != seal(s) {
		return errors.New("invalid or incomplete v2 checkpoint")
	}
	var prev uint
	for _, e := range s.Entries {
		if e.ID == 0 || e.ID <= prev || !digestPattern.MatchString(e.Semantic) || !digestPattern.MatchString(e.Observation) || !digestPattern.MatchString(e.Dependency) || (e.Freshness != "current" && e.Freshness != "stale_open" && e.Freshness != "recovered") {
			return errors.New("invalid checkpoint entry")
		}
		prev = e.ID
	}
	return nil
}
func Compare(old *Snapshot, current Snapshot) (Report, error) {
	if err := current.Validate(); err != nil {
		return Report{}, err
	}
	r := Report{Version: Version, CurrentSHA: current.SHA, Counts: map[string]int{}, Changes: []Change{}, NoActionExecuted: true, Limitations: []string{"Inventory comparison only; no root-cause, identity certification, training admission or outcome maturity decision", "Source namespace is an operator-declared stable source identifier, not cryptographic database identity", "Missing IDs are source discrepancies, never recovery or negative labels", "v1 lacks required comparison fields; establish v2 baseline before skipping work"}}
	baseline := map[uint]Entry{}
	compatible := old != nil
	if old != nil {
		// Legacy/incomplete checkpoints cannot justify skipping any current item.
		if old.Version == Version {
			if err := old.Validate(); err != nil {
				return Report{}, err
			}
			r.PreviousSHA = old.SHA
			if current.AsOf.Before(old.AsOf) {
				return Report{}, errors.New("checkpoint time moved backwards")
			}
		} else {
			compatible = false
		}
		if old.Namespace != current.Namespace {
			compatible = false
		}
		if compatible {
			for _, e := range old.Entries {
				baseline[e.ID] = e
			}
		}
	}
	for _, e := range current.Entries {
		c := Change{ID: e.ID, Action: "new", Reasons: []string{"not_in_previous_checkpoint"}}
		if !compatible {
			c.Action = "comparison_unknown"
			c.Reasons = []string{"establish_v2_baseline_or_source_changed"}
		} else if p, ok := baseline[e.ID]; ok {
			c.Action = "unchanged"
			c.Reasons = []string{}
			if e.Semantic != p.Semantic {
				c.Action = "changed"
				c.Reasons = append(c.Reasons, "event_semantics_or_evidence_changed")
			}
			if e.Dependency != p.Dependency {
				c.Action = "changed"
				c.Reasons = append(c.Reasons, "associated_evidence_changed")
			}
			if e.Freshness != p.Freshness {
				c.Action = "changed"
				c.Reasons = append(c.Reasons, "freshness_boundary_changed")
			}
			if c.Action == "unchanged" && e.Observation != p.Observation {
				c.Action = "observation_only"
				c.Reasons = []string{"observation_progress_only"}
			}
			delete(baseline, e.ID)
		}
		r.Changes = append(r.Changes, c)
		r.Counts[c.Action]++
	}
	for id := range baseline {
		r.Changes = append(r.Changes, Change{id, "missing", []string{"absent_from_complete_current_inventory"}})
		r.Counts["missing"]++
	}
	sort.Slice(r.Changes, func(i, j int) bool { return r.Changes[i].ID < r.Changes[j].ID })
	return r, nil
}
func fields(v any, omit ...string) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var m map[string]any
	if err = json.Unmarshal(b, &m); err != nil {
		panic(err)
	}
	for _, k := range omit {
		delete(m, k)
	}
	return m
}
func checkTime(asof time.Time) error {
	if asof.IsZero() {
		return fmt.Errorf("explicit as-of required")
	}
	return nil
}
