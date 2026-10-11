package nodeaccess

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
)

// ReadAcceptanceAudit checks serialization only. SHA fingerprints in an imported
// audit establish consistency, not authenticity or operator authorization.
func ReadAcceptanceAudit(r io.Reader) (AcceptanceAudit, error) {
	var audit AcceptanceAudit
	raw, err := io.ReadAll(io.LimitReader(r, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return audit, errors.New("audit unreadable or larger than 2 MiB")
	}
	if err := checkScopeJSON(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return audit, errors.New("invalid or ambiguous audit JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&audit); err != nil {
		return audit, errors.New("invalid audit fields")
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return audit, errors.New("audit must contain one JSON object")
	}
	return audit, nil
}

// ValidateAcceptancePlan reconstructs the finite plan at its historical capture
// time. It cannot execute a command or accept caller-supplied shell fragments.
func ValidateAcceptancePlan(plan AcceptancePlan, at time.Time) error {
	scope := AcceptanceScope{Schema: "hardware-evidence-scope-v1", ScopeID: plan.ScopeID,
		Node: plan.Node, Username: "audit-replay", CredentialProfileID: plan.CredentialProfileID,
		IssuedAt: plan.IssuedAt, ExpiresAt: plan.ExpiresAt}
	for _, command := range plan.Commands {
		request := AcceptanceRequest{ID: command.ID}
		switch {
		case command.ID == "disk.smart":
			if len(command.Argv) < 3 {
				return errors.New("invalid SMART argv")
			}
			request.Protocol, request.Device = command.Argv[2], command.Argv[len(command.Argv)-1]
		case strings.HasPrefix(command.ID, "memory.edac."):
			found := false
			for index := 0; index <= 31; index++ {
				candidate := AcceptanceRequest{ID: command.ID, Controller: &index}
				template, err := acceptanceTemplate(candidate)
				if err == nil && reflect.DeepEqual(template, command) {
					value := index
					request.Controller, found = &value, true
					break
				}
			}
			if !found {
				return errors.New("invalid EDAC argv")
			}
		}
		scope.Commands = append(scope.Commands, request)
	}
	reconstructed, err := PlanAcceptance(scope, at)
	if err != nil || !reflect.DeepEqual(reconstructed, plan) {
		return errors.New("audit plan differs from the registered read-only plan")
	}
	return nil
}
