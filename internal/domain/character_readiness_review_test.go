package domain_test

import (
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/chenhongyang/novel-studio/internal/testutil"
)

func TestReadinessChecksDueContractsWithoutForcingFutureEnding(t *testing.T) {
	_, _, _, input := testutil.CharacterReadiness(t, false)
	verdict := testutil.ReadyVerdict(input)
	r, err := domain.FinalizeCharacterReadinessReview(input, verdict)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != domain.CharacterReadinessReviewedVersion || r.InputDigest == "" || r.Decision != "ready_for_plan" {
		t.Fatal("missing reviewed readiness binding")
	}
	if err := domain.ValidateCharacterReadinessReviewAudit(domain.CharacterReadinessReviewAudit{Input: input, Receipt: r}); err != nil {
		t.Fatal(err)
	}
	_, _, _, finalInput := testutil.CharacterReadiness(t, true)
	if _, err := domain.FinalizeCharacterReadinessReview(finalInput, verdict); err == nil {
		t.Fatal("final chapter accepted an ending deferred to the future")
	}
}

// The chapter's frozen core event must become due in the chapter that owns it.
// Book-level hard contracts legitimately mature only in the final chapter; a
// frozen chapter event that inherited that rule was effectively never due, so
// locally rational character agents could skip an authored, theme-bearing scene
// and the plan then had to be rewritten around the omission.
func TestFrozenCoreEventContractIsDueInItsOwnChapter(t *testing.T) {
	event := domain.FrozenCoreEventContractText("沈大同用十块灵石替赵无恤赎回铁剑")
	for _, want := range []string{domain.CharacterFrozenCoreEventContractPrefix, "沈大同用十块灵石替赵无恤赎回铁剑", "不得标为 infeasible", "不得把它改写成别的事件"} {
		if !strings.Contains(event, want) {
			t.Fatalf("frozen core event contract lost %q: %s", want, event)
		}
	}
	context, err := domain.FinalizeCharacterReadinessContext(domain.CharacterReadinessContext{
		GenerationID: "pg2_frozen_core_fixture", Chapter: 1, POVCharacter: "沈大同",
		ArcLastChapter: 3, BookLastChapter: 304,
		SoftOutline:   domain.OutlineEntry{Chapter: 1, Title: "老槐树下，等一扇门", CoreEvent: "沈大同用十块灵石替赵无恤赎回铁剑"},
		HardContracts: []string{event, "保留全书结局方向"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !domain.IsFrozenCoreEventContract(event) || domain.IsFrozenCoreEventContract("保留全书结局方向") {
		t.Fatal("frozen core event contract identity is not stable")
	}
	requirements, err := domain.CharacterReadinessRequirements(context)
	if err != nil {
		t.Fatal(err)
	}
	due := map[string]bool{}
	for _, requirement := range requirements {
		due[requirement.Contract] = requirement.DueNow
	}
	if !due[event] {
		t.Fatal("本章冻结核心事件在所属章没有到期，推演可以合法跳过它")
	}
	if due["保留全书结局方向"] {
		t.Fatal("book-level hard contract was pulled forward into chapter 1")
	}
}

func TestReadinessRejectsMissingInventedOrOnlyIntendedEvidence(t *testing.T) {
	for _, mode := range []string{"missing-check", "pending-due", "unknown-ref", "proposal-only", "limit-is-conflict", "altered-requirements", "altered-context"} {
		t.Run(mode, func(t *testing.T) {
			_, _, _, input := testutil.CharacterReadiness(t, false)
			verdict := testutil.ReadyVerdict(input)
			switch mode {
			case "missing-check":
				verdict.ContractChecks = verdict.ContractChecks[:1]
			case "pending-due":
				verdict.ContractChecks[1].Status = "pending"
			case "unknown-ref":
				verdict.EvidenceRefs = []string{"invented-result"}
			case "proposal-only":
				verdict.EvidenceRefs = []string{input.Trace.Cycles[0].Actions[0].ProposalDigest}
			case "limit-is-conflict":
				input.RemainingCycles = 0
				verdict.Decision = "hard_conflict"
			case "altered-requirements":
				input.Requirements[1].DueNow = false
			case "altered-context":
				input.Context.HardContracts = nil
			}
			if _, err := domain.FinalizeCharacterReadinessReview(input, verdict); err == nil {
				t.Fatal("invalid readiness verdict accepted")
			}
		})
	}
}

func TestReadinessReceiptTamperingFailsVerification(t *testing.T) {
	_, _, _, input := testutil.CharacterReadiness(t, false)
	r, err := domain.FinalizeCharacterReadinessReview(input, testutil.ReadyVerdict(input))
	if err != nil {
		t.Fatal(err)
	}
	r.Reason = "重新解释但保留旧摘要"
	if err := domain.ValidateCharacterReadinessReviewAudit(domain.CharacterReadinessReviewAudit{Input: input, Receipt: r}); err == nil {
		t.Fatal("readiness receipt tampering passed")
	}
}
