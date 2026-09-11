package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// sortedCharacterReadinessRefs renders a bounded, stable list of the references
// this review accepts, so a rejection can name the legal values instead of only
// the rule. Terse feedback ("cites evidence outside its exact input") left the
// model guessing which digest to cite and cost three resubmissions on chapter 1.
func sortedCharacterReadinessRefs(refs map[string]bool, limit int) string {
	all := make([]string, 0, len(refs))
	for ref := range refs {
		if strings.TrimSpace(ref) != "" {
			all = append(all, ref)
		}
	}
	sort.Strings(all)
	if len(all) == 0 {
		return "（本轮尚无可引用证据）"
	}
	shown := all
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	return strings.Join(shown, ", ")
}

const CharacterReadinessReviewPolicy = "chapter-readiness:arbitrated-events.v1"
const CharacterReadinessReviewedVersion = "character-chapter-readiness.v2"

// CharacterFrozenCoreEventContractPrefix marks a hard contract that binds the
// chapter's frozen projected core event. Unlike the book-level hard contracts
// (due only in the final chapter), this one is due in the chapter that owns it:
// the arbitration has to make the event actually happen, with only its staging,
// order, cost and side effects left to the characters.
const CharacterFrozenCoreEventContractPrefix = "本章冻结核心事件（终态不可改变；角色只能决定如何发生、付出什么代价，不可落空、替换或推迟到别章）："

// IsFrozenCoreEventContract reports whether a hard contract binds the chapter's
// own frozen core event, so readiness reaches it in the chapter that owns it.
func IsFrozenCoreEventContract(contract string) bool {
	return strings.HasPrefix(strings.TrimSpace(contract), CharacterFrozenCoreEventContractPrefix)
}

// FrozenCoreEventContractText renders the host-owned hard contract that binds one
// chapter's frozen projected core event. The contract carries its own
// adjudication and readiness semantics, so no frozen model protocol has to
// change: the event is due in its own chapter, only its terminal state is fixed,
// and a first-round proposal that makes it impossible must go back to the
// characters for revision instead of being declared infeasible or rewritten into
// another event. Leaving this event to locally rational agents silently replaced
// authored, theme-bearing scenes with survival calculus.
func FrozenCoreEventContractText(event string) string {
	return CharacterFrozenCoreEventContractPrefix + strings.TrimSpace(event) +
		`。本章到期项：必须由本章实际裁决的事件兑现该终态；顺序、场合、实际用时、代价、参与者与可观测后果可以按角色的真实提案不同。第一轮提案使该事件不可能发生时，finalized=false 并给相关角色最小冲突反馈，要求其就本人在该事件中的做法重新提案；不得标为 infeasible，不得把它改写成别的事件，也不得用意图、承诺、打算或离屏计划代替实际发生。`
}

// This is a frozen host planning context, not a character observation. The
// session binds its digest before the first cycle, so a later outline edit
// cannot silently change what an already paid readiness review was judging.
type CharacterReadinessContext struct {
	Version                 string                          `json:"version"`
	GenerationID            string                          `json:"generation_id"`
	Chapter                 int                             `json:"chapter"`
	POVCharacter            string                          `json:"pov_character"`
	ArcLastChapter          int                             `json:"arc_last_chapter"`
	BookLastChapter         int                             `json:"book_last_chapter"`
	TargetWords             int                             `json:"target_words,omitempty"`
	ProjectionContextDigest string                          `json:"projection_context_digest,omitempty"`
	SoftOutline             OutlineEntry                    `json:"soft_outline"`
	HardContracts           []string                        `json:"hard_contracts"`
	Obligations             []ProjectedPlanningObligationV2 `json:"obligations,omitempty"`
	Digest                  string                          `json:"digest"`
}

func FinalizeCharacterReadinessContext(value CharacterReadinessContext) (CharacterReadinessContext, error) {
	if value.Version == "" {
		value.Version = CharacterReadinessReviewPolicy
	}
	if value.Version != CharacterReadinessReviewPolicy || !strings.HasPrefix(value.GenerationID, PlanningGenerationIDPrefix) || value.Chapter < 1 || value.ArcLastChapter < value.Chapter || value.BookLastChapter < value.ArcLastChapter || value.SoftOutline.Chapter != value.Chapter || strings.TrimSpace(value.POVCharacter) == "" || value.TargetWords < 0 {
		return value, fmt.Errorf("chapter readiness context has invalid identity/boundaries")
	}
	if value.ProjectionContextDigest != "" {
		if err := validatePlanningV2Digest("readiness projection context", value.ProjectionContextDigest); err != nil {
			return value, err
		}
	}
	value.HardContracts = normalizeV2Strings(value.HardContracts)
	seen := map[string]bool{}
	for _, obligation := range value.Obligations {
		if strings.TrimSpace(obligation.ID) == "" || seen[obligation.ID] || strings.TrimSpace(obligation.Contract) == "" {
			return value, fmt.Errorf("readiness context has invalid/duplicate obligations")
		}
		seen[obligation.ID] = true
	}
	value.Digest = ""
	var err error
	value.Digest, err = characterAgentDigest(value)
	return value, err
}

type CharacterReadinessAction struct {
	AgentID         string `json:"agent_id"`
	Character       string `json:"character"`
	ProposalDigest  string `json:"proposal_digest"`
	Decision        string `json:"decision"`
	IntendedAction  string `json:"intended_action"`
	Outcome         string `json:"outcome"`
	CompletionState string `json:"completion_state"`
	ImmediateResult string `json:"immediate_result"`
	StateAfter      string `json:"state_after"`
}

type CharacterReadinessCycleView struct {
	Index                 int                        `json:"index"`
	CycleDigest           string                     `json:"cycle_digest"`
	ArbitrationDigest     string                     `json:"arbitration_digest"`
	StoryTime             *StoryTimeChapterSchedule  `json:"story_time"`
	Actions               []CharacterReadinessAction `json:"actions"`
	HardContractStatus    string                     `json:"hard_contract_status"`
	HardContractConflicts []string                   `json:"hard_contract_conflicts,omitempty"`
}

type CharacterReadinessActorView struct {
	OperationalObservations []CharacterOperationalObservationV1 `json:"operational_observations,omitempty"`
	AgentID                 string                              `json:"agent_id"`
	Character               string                              `json:"character"`
	Location                string                              `json:"location"`
	Resources               []CharacterResourceViewV2           `json:"resources"`
	TaskProgress            []CharacterTaskProgressV2           `json:"task_progress"`
	ReceivedFacts           []CharacterReceivedFactV2           `json:"received_facts"`
}

// Only the final state is included, once. Individual cycles keep exact intent
// and result text but do not repeat every actor's full physical/memory history.
type CharacterReadinessTrace struct {
	Cycles            []CharacterReadinessCycleView `json:"cycles"`
	FinalPhysicalRoot string                        `json:"final_physical_root"`
	Resources         []WorldResourceBalanceV2      `json:"world_resources"`
	Actors            []CharacterReadinessActorView `json:"actors"`
}

func BuildCharacterReadinessTrace(cycles []CharacterActivationCycle) (CharacterReadinessTrace, error) {
	var trace CharacterReadinessTrace
	if len(cycles) == 0 {
		return trace, fmt.Errorf("readiness requires actual cycle evidence")
	}
	for i, cycle := range cycles {
		if err := ValidateCharacterActivationCycle(cycle); err != nil {
			return trace, err
		}
		if cycle.Index != i+1 || (i > 0 && (cycle.PreviousDigest != cycles[i-1].Digest || cycle.GenerationID != cycles[0].GenerationID || cycle.Chapter != cycles[0].Chapter || cycle.BeforePhysicalRoot != cycles[i-1].AfterPhysicalRoot || cycle.StartDay != cycles[i-1].EndDay)) {
			return trace, fmt.Errorf("readiness cycle chain is incomplete")
		}
		receipt := cycle.Evidence.Arbitrations[len(cycle.Evidence.Arbitrations)-1]
		view := CharacterReadinessCycleView{Index: cycle.Index, CycleDigest: cycle.Digest, ArbitrationDigest: receipt.Digest, StoryTime: receipt.StoryTime, HardContractStatus: receipt.HardContractStatus, HardContractConflicts: receipt.HardContractConflicts}
		for _, resolution := range receipt.Resolutions {
			view.Actions = append(view.Actions, CharacterReadinessAction{resolution.AgentID, resolution.Character, resolution.ProposalDigest, resolution.Decision, resolution.IntendedAction, resolution.Outcome, resolution.CompletionState, resolution.ImmediateResult, resolution.StateAfter})
		}
		trace.Cycles = append(trace.Cycles, view)
	}
	last := cycles[len(cycles)-1]
	receipt := last.Evidence.Arbitrations[len(last.Evidence.Arbitrations)-1]
	state, err := ApplyArbitrationPhysicalStateV2(receipt, last.Evidence.Stimulus, LatestCharacterCycleProposals(last.Evidence)...)
	if err != nil {
		return trace, err
	}
	trace.FinalPhysicalRoot, trace.Resources = last.AfterPhysicalRoot, state.Resources
	for _, actor := range state.Actors {
		resources, err := BuildCharacterResourceViewsV2(state, actor.AgentID)
		if err != nil {
			return trace, err
		}
		operations, err := selectCharacterOperationalObservationsV1(actor.OperationalObservations)
		if err != nil {
			return trace, err
		}
		trace.Actors = append(trace.Actors, CharacterReadinessActorView{AgentID: actor.AgentID, Character: actor.Character, Location: actor.Location, Resources: resources, TaskProgress: actor.TaskProgress, ReceivedFacts: actor.ReceivedFacts, OperationalObservations: operations})
	}
	return trace, nil
}

type CharacterReadinessRequirement struct {
	ID       string `json:"id"`
	Contract string `json:"contract"`
	DueNow   bool   `json:"due_now"`
}

type CharacterReadinessContractCheck struct {
	ContractID   string   `json:"contract_id"`
	Status       string   `json:"status"` // satisfied / preserved / pending / impossible
	EvidenceRefs []string `json:"evidence_refs"`
}

type CharacterReadinessVerdict struct {
	Decision       string                            `json:"decision"`
	Reason         string                            `json:"reason"`
	EvidenceRefs   []string                          `json:"evidence_refs"`
	ContractChecks []CharacterReadinessContractCheck `json:"contract_checks"`
}

type CharacterReadinessReviewInput struct {
	Policy          string                          `json:"policy"`
	ReviewProtocol  string                          `json:"review_protocol"`
	SessionDigest   string                          `json:"session_digest"`
	Context         CharacterReadinessContext       `json:"chapter_context"`
	Trace           CharacterReadinessTrace         `json:"actual_events"`
	Requirements    []CharacterReadinessRequirement `json:"required_checks"`
	RemainingCycles int                             `json:"remaining_cycles"`
}

type CharacterReadinessReviewAudit struct {
	Input     CharacterReadinessReviewInput     `json:"input"`
	Receipt   CharacterChapterReadiness         `json:"receipt"`
	ModelView *CharacterReadinessModelBindingV1 `json:"model_view,omitempty"`
}

func NewCharacterReadinessReviewInput(context CharacterReadinessContext, session CharacterActivationSession, cycles []CharacterActivationCycle, reviewProtocol string) (CharacterReadinessReviewInput, error) {
	var input CharacterReadinessReviewInput
	checked, err := FinalizeCharacterReadinessContext(context)
	if err != nil {
		return input, err
	}
	if checked.Digest != context.Digest || context.Digest != session.ChapterContextDigest || context.GenerationID != session.GenerationID || context.Chapter != session.Chapter {
		return input, fmt.Errorf("readiness context is not the session's frozen chapter context")
	}
	if err := ValidateCharacterActivationSession(session); err != nil {
		return input, err
	}
	if session.Phase != "assessing" || len(cycles) != len(session.CycleDigests) {
		return input, fmt.Errorf("readiness must assess the exact pending cycle chain")
	}
	for i := range cycles {
		if cycles[i].Digest != session.CycleDigests[i] {
			return input, fmt.Errorf("readiness cycle differs from committed session")
		}
	}
	trace, err := BuildCharacterReadinessTrace(cycles)
	if err != nil {
		return input, err
	}
	if err := validatePlanningV2Digest("readiness protocol", reviewProtocol); err != nil {
		return input, err
	}
	input = CharacterReadinessReviewInput{Policy: CharacterReadinessReviewPolicy, ReviewProtocol: reviewProtocol, SessionDigest: session.Digest, Context: context, Trace: trace, RemainingCycles: session.MaxCycles - len(cycles)}
	input.Requirements, err = CharacterReadinessRequirements(context)
	return input, err
}

func CharacterReadinessRequirements(context CharacterReadinessContext) ([]CharacterReadinessRequirement, error) {
	var requirements []CharacterReadinessRequirement
	for _, contract := range context.HardContracts {
		hash, err := characterAgentDigest(contract)
		if err != nil {
			return nil, err
		}
		// Book-level hard contracts mature at the end of the book; the chapter's
		// own frozen core event matures in the chapter that owns it.
		dueNow := context.Chapter == context.BookLastChapter || IsFrozenCoreEventContract(contract)
		requirements = append(requirements, CharacterReadinessRequirement{ID: "hard_" + strings.TrimPrefix(hash, "sha256:"), Contract: contract, DueNow: dueNow})
	}
	for _, obligation := range context.Obligations {
		if obligation.Hardness != ObligationHardnessV2("hard") {
			continue
		}
		requirements = append(requirements, CharacterReadinessRequirement{ID: obligation.ID, Contract: obligation.Contract, DueNow: obligation.DueNow})
	}
	return requirements, nil
}

func CharacterReadinessReviewInputDigest(input CharacterReadinessReviewInput) (string, error) {
	if input.Policy != CharacterReadinessReviewPolicy || len(input.Trace.Cycles) == 0 {
		return "", fmt.Errorf("invalid readiness review input")
	}
	context, err := FinalizeCharacterReadinessContext(input.Context)
	if err != nil {
		return "", err
	}
	if context.Digest != input.Context.Digest || input.RemainingCycles < 0 {
		return "", fmt.Errorf("readiness context/bounds mismatch")
	}
	for _, digest := range []string{input.ReviewProtocol, input.SessionDigest, input.Trace.FinalPhysicalRoot} {
		if err := validatePlanningV2Digest("readiness source binding", digest); err != nil {
			return "", err
		}
	}
	requirements, err := CharacterReadinessRequirements(context)
	if err != nil {
		return "", err
	}
	left, _ := json.Marshal(requirements)
	right, _ := json.Marshal(input.Requirements)
	if string(left) != string(right) {
		return "", fmt.Errorf("readiness input altered the frozen hard requirements")
	}
	for i, cycle := range input.Trace.Cycles {
		if cycle.Index != i+1 || cycle.StoryTime == nil || cycle.StoryTime.Chapter != input.Context.Chapter {
			return "", fmt.Errorf("readiness trace has invalid cycle identity/time")
		}
		for _, digest := range []string{cycle.CycleDigest, cycle.ArbitrationDigest} {
			if err := validatePlanningV2Digest("readiness cycle source", digest); err != nil {
				return "", err
			}
		}
	}
	return characterAgentDigest(input)
}

func FinalizeCharacterReadinessReview(input CharacterReadinessReviewInput, verdict CharacterReadinessVerdict) (CharacterChapterReadiness, error) {
	var result CharacterChapterReadiness
	inputDigest, err := CharacterReadinessReviewInputDigest(input)
	if err != nil {
		return result, err
	}
	allowed := map[string]bool{input.Trace.FinalPhysicalRoot: true}
	actual := map[string]bool{input.Trace.FinalPhysicalRoot: true}
	for _, cycle := range input.Trace.Cycles {
		allowed[cycle.CycleDigest], allowed[cycle.ArbitrationDigest] = true, true
		actual[cycle.CycleDigest], actual[cycle.ArbitrationDigest] = true, true
		for _, action := range cycle.Actions {
			allowed[action.ProposalDigest] = true
		}
	}
	validateRefs := func(refs []string) error {
		candidates := sortedCharacterReadinessRefs(actual, 8)
		if len(refs) == 0 || len(refs) > 12 {
			return fmt.Errorf("readiness must cite 1-12 actual evidence references。可引用的已绑定证据共 %d 个：%s", len(actual), candidates)
		}
		actualCited := false
		for _, ref := range refs {
			if !allowed[ref] {
				return fmt.Errorf("readiness cites evidence outside its exact input：%q 不在可用清单里。可引用的已绑定证据共 %d 个，前 8 个：%s", ref, len(actual), candidates)
			}
			actualCited = actualCited || actual[ref]
		}
		if !actualCited {
			return fmt.Errorf("a proposal alone cannot prove readiness; cite an actual cycle/arbitration/state。可引用的已绑定证据共 %d 个，前 8 个：%s", len(actual), candidates)
		}
		return nil
	}
	if err := validateRefs(verdict.EvidenceRefs); err != nil {
		return result, err
	}
	checks := map[string]CharacterReadinessContractCheck{}
	for _, check := range verdict.ContractChecks {
		if _, duplicate := checks[check.ContractID]; duplicate {
			return result, fmt.Errorf("duplicate readiness contract check")
		}
		if err := validateRefs(check.EvidenceRefs); err != nil {
			return result, err
		}
		checks[check.ContractID] = check
	}
	if len(checks) != len(input.Requirements) {
		return result, fmt.Errorf("readiness must assess every hard requirement exactly once")
	}
	var impossible []string
	for _, requirement := range input.Requirements {
		check, ok := checks[requirement.ID]
		if !ok {
			return result, fmt.Errorf("readiness omitted a hard requirement")
		}
		switch check.Status {
		case "satisfied", "preserved", "pending", "impossible":
		default:
			return result, fmt.Errorf("invalid readiness contract status")
		}
		if check.Status == "impossible" {
			impossible = append(impossible, requirement.ID)
		}
		if verdict.Decision == "ready_for_plan" && ((requirement.DueNow && check.Status != "satisfied") || check.Status == "impossible") {
			return result, fmt.Errorf("unfulfilled due/impossible hard requirement cannot authorize chapter planning")
		}
	}
	last := input.Trace.Cycles[len(input.Trace.Cycles)-1]
	if last.HardContractStatus == "infeasible" {
		if verdict.Decision != "hard_conflict" {
			return result, fmt.Errorf("readiness cannot override an arbitrated hard conflict")
		}
		impossible = append(impossible, last.HardContractConflicts...)
	}
	if verdict.Decision == "hard_conflict" && len(impossible) == 0 {
		return result, fmt.Errorf("hard conflict requires an impossible hard contract, not a cycle/budget limit")
	}
	if verdict.Decision != "hard_conflict" && len(impossible) > 0 {
		return result, fmt.Errorf("impossible hard contracts require hard_conflict")
	}
	result = CharacterChapterReadiness{Version: CharacterReadinessReviewedVersion, GenerationID: input.Context.GenerationID, Chapter: input.Context.Chapter, CycleDigest: last.CycleDigest, ReviewProtocol: input.ReviewProtocol, InputDigest: inputDigest, Decision: verdict.Decision, Reason: verdict.Reason, EvidenceRefs: verdict.EvidenceRefs, ContractChecks: verdict.ContractChecks, UnresolvedHardContracts: impossible}
	return FinalizeCharacterChapterReadiness(result)
}

func ValidateCharacterReadinessReviewAudit(audit CharacterReadinessReviewAudit) error {
	if audit.ModelView != nil {
		codec, err := NewCharacterReadinessModelCodecV1(audit.Input)
		if err != nil {
			return err
		}
		if *audit.ModelView != codec.Binding() {
			return fmt.Errorf("readiness model view differs from its canonical input")
		}
	}
	want, err := FinalizeCharacterReadinessReview(audit.Input, CharacterReadinessVerdict{audit.Receipt.Decision, audit.Receipt.Reason, audit.Receipt.EvidenceRefs, audit.Receipt.ContractChecks})
	if err != nil {
		return err
	}
	left, _ := json.Marshal(want)
	right, _ := json.Marshal(audit.Receipt)
	if string(left) != string(right) {
		return fmt.Errorf("readiness review receipt/input mismatch")
	}
	return nil
}
