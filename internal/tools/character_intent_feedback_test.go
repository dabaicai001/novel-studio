package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/domain"
)

// The decision and its intent belong to the character agent, not to the arbiter:
// the host owns them exactly like the bound character identity. A paraphrasing
// arbiter is therefore normalized to the proposal's exact text instead of being
// rejected — rejecting made every retry re-emit the whole receipt and repair one
// character per turn until the turn budget was gone (measured on chapter 1 of the
// project-all stage). This test pins the replacement, the absence of the rejected
// candidate text in the stored receipt, and byte equality with the legal receipt.
func TestWorldToolIntentRewriteIsNormalizedToTheProposalWithoutLeakingCandidate(t *testing.T) {
	for _, mode := range []string{"decision", "intended_action", "both"} {
		t.Run(mode, func(t *testing.T) {
			st, session, cycle, proofs := activationToolFixture(t, true)
			e := cycle.Evidence
			p := e.Proposals[0]
			tool, err := NewResolveCharacterActivationTool(st, session, e.Stimulus, e.Activation, e.Proposals, e.ProtocolDigest, nil, 1)
			if err != nil {
				t.Fatal(err)
			}
			valid := activationArbiterArgs(t, cycle)
			var args map[string]json.RawMessage
			if err := json.Unmarshal(valid, &args); err != nil {
				t.Fatal(err)
			}
			var resolutions []map[string]json.RawMessage
			if err := json.Unmarshal(args["resolutions"], &resolutions); err != nil {
				t.Fatal(err)
			}
			if mode != "intended_action" {
				resolutions[0]["decision"], _ = json.Marshal("PRIVATE_CANDIDATE_DECISION")
			}
			if mode != "decision" {
				resolutions[0]["intended_action"], _ = json.Marshal("PRIVATE_CANDIDATE_ACTION")
			}
			args["resolutions"], _ = json.Marshal(resolutions)
			raw, _ := json.Marshal(args)

			if _, err := tool.Execute(context.Background(), raw); err != nil {
				t.Fatalf("rewritten intent must be host-normalized, not rejected: %v", err)
			}
			receipt, err := proofs.LoadArbitration(e.GenerationID, e.Chapter, 1)
			if err != nil || receipt == nil {
				t.Fatalf("normalized arbitration missing: %v", err)
			}
			restored := false
			for _, resolution := range receipt.Resolutions {
				if resolution.AgentID != p.AgentID {
					continue
				}
				restored = true
				if resolution.Decision != p.Decision || resolution.IntendedAction != p.IntendedAction {
					t.Fatalf("host did not restore the bound proposal text: %#v", resolution)
				}
			}
			if !restored {
				t.Fatal("normalized arbitration lost the bound resolution")
			}
			stored, _ := json.Marshal(receipt)
			if strings.Contains(string(stored), "PRIVATE_CANDIDATE") {
				t.Fatal("normalized receipt leaked the rejected candidate text")
			}

			expected := e.Arbitrations[0]
			expected.GeneratedAt = receipt.GeneratedAt
			expected, err = domain.FinalizeWorldArbitrationReceipt(expected, e.Stimulus, e.Activation, e.Proposals, 1)
			if err != nil {
				t.Fatal(err)
			}
			expectedJSON, _ := json.Marshal(expected)
			actualJSON, _ := json.Marshal(receipt)
			if expected.Digest != receipt.Digest || string(expectedJSON) != string(actualJSON) {
				t.Fatal("normalization changed the legal receipt bytes/digest")
			}
			if !reflect.DeepEqual(receipt.Resolutions[0].Decision, p.Decision) {
				t.Fatal("bound decision was not authoritative")
			}
		})
	}
}
