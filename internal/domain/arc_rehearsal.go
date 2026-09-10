package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

const ArcRehearsalVersion = "arc-rehearsal.v1"

// Rehearsal is an author-side conditional forecast, never an execution receipt.
type ArcRehearsalInput struct {
	Version               string                               `json:"version"`
	ProtocolDigest        string                               `json:"protocol_digest,omitempty"`
	ExecutionCapabilities *ArcRehearsalExecutionCapabilitiesV1 `json:"execution_capabilities,omitempty"`
	ArcID                 string                               `json:"arc_id"`
	ArcFirstChapter       int                                  `json:"arc_first_chapter"`
	ArcLastChapter        int                                  `json:"arc_last_chapter"`
	BaseCanonChapter      int                                  `json:"base_canon_chapter"`
	BaseCanonRoot         string                               `json:"base_canon_root"`
	SourceRoot            string                               `json:"source_root"`
	SourceFiles           map[string]string                    `json:"source_files"`
	Outline               []OutlineEntry                       `json:"outline"`
	CharacterObservations []CharacterObservationPacket         `json:"character_observations"`
	WorldRules            []WorldRule                          `json:"world_rules"`
	WorldCodex            *WorldCodex                          `json:"world_codex"`
	BookWorld             *BookWorld                           `json:"book_world"`
	WorldState            *WorldPhysicalStateV2                `json:"world_state"`
	HardContracts         []string                             `json:"hard_contracts"`
	UserRules             json.RawMessage                      `json:"user_rules"`
	AcceptedSummaries     []ChapterSummary                     `json:"accepted_summaries,omitempty"`
	AcceptedEvidence      map[int]string                       `json:"accepted_evidence,omitempty"`
	InputDigest           string                               `json:"input_digest"`
}

type ArcRehearsalChapter struct {
	Chapter              int      `json:"chapter"`
	ConditionalForecast  string   `json:"conditional_forecast"`
	Assumptions          []string `json:"assumptions"`
	CausalLinks          []string `json:"causal_links"`
	TimeResourceChecks   []string `json:"time_resource_checks"`
	AcceptedSourceDigest string   `json:"accepted_source_digest,omitempty"`
}

type ArcRehearsalCharacterConflict struct {
	Character          string   `json:"character"`
	CurrentGoal        string   `json:"current_goal"`
	Conflicts          []string `json:"conflicts"`
	ConditionalChoices []string `json:"conditional_choices"`
}

type ArcRehearsalContractCheck struct {
	Contract   string   `json:"contract"`
	Assessment string   `json:"assessment"` // plausible / conditional / unresolved / infeasible_prediction
	Conditions []string `json:"conditions"`
}

type ArcRehearsalMaterialCheck struct {
	CapabilityRequirements []ArcRehearsalCapabilityRequirementV1 `json:"capability_requirements,omitempty"`
	Operation              string                                `json:"operation"`
	RequiresReadable       bool                                  `json:"requires_readable"`
	ResourceRefs           []string                              `json:"resource_refs"`
	Status                 string                                `json:"status"` // available / missing / unclear / not_required
	Explanation            string                                `json:"explanation"`
}

type ArcRehearsalBody struct {
	Summary            string                          `json:"summary"`
	Chapters           []ArcRehearsalChapter           `json:"chapters"`
	CharacterConflicts []ArcRehearsalCharacterConflict `json:"character_conflicts"`
	ContractChecks     []ArcRehearsalContractCheck     `json:"contract_checks"`
	MaterialChecks     []ArcRehearsalMaterialCheck     `json:"material_checks"`
	UnresolvedItems    []string                        `json:"unresolved_items"`
}

// These identifiers are filled from the actual observed model response and
// existing usage hooks by the runner; the submission schema cannot author them.
type ArcRehearsalCall struct {
	Role           string   `json:"role"`
	Provider       string   `json:"provider"`
	Model          string   `json:"model"`
	UsageIDs       []string `json:"usage_ids"`
	ToolCallID     string   `json:"tool_call_id"`
	ResponseDigest string   `json:"response_digest"`
}

type ArcRehearsalDraft struct {
	Version     string           `json:"version"`
	Authority   string           `json:"authority"`
	InputDigest string           `json:"input_digest"`
	Body        ArcRehearsalBody `json:"body"`
	Call        ArcRehearsalCall `json:"call"`
	DraftDigest string           `json:"draft_digest"`
}

type ArcRehearsalReport struct {
	Version          string           `json:"version"`
	Authority        string           `json:"authority"`
	ArcID            string           `json:"arc_id"`
	ArcFirstChapter  int              `json:"arc_first_chapter"`
	ArcLastChapter   int              `json:"arc_last_chapter"`
	BaseCanonChapter int              `json:"base_canon_chapter"`
	BaseCanonRoot    string           `json:"base_canon_root"`
	SourceRoot       string           `json:"source_root"`
	InputDigest      string           `json:"input_digest"`
	DraftDigest      string           `json:"draft_digest"`
	ReadyForDetail   bool             `json:"ready_for_detail"`
	Body             ArcRehearsalBody `json:"body"`
	Call             ArcRehearsalCall `json:"call"`
	ReportDigest     string           `json:"report_digest"`
}

func FinalizeArcRehearsalInput(input ArcRehearsalInput) (ArcRehearsalInput, error) {
	if input.Version == "" {
		input.Version = ArcRehearsalVersion
	}
	if input.Version != ArcRehearsalVersion || strings.TrimSpace(input.ArcID) == "" || input.ArcFirstChapter < 1 || input.ArcLastChapter < input.ArcFirstChapter || input.BaseCanonChapter < input.ArcFirstChapter-1 || input.BaseCanonChapter >= input.ArcLastChapter || !characterSourceDigestPatternV2.MatchString(input.BaseCanonRoot) || !characterSourceDigestPatternV2.MatchString(input.SourceRoot) {
		return input, fmt.Errorf("arc rehearsal requires exact arc, accepted prefix and source identities")
	}
	// Missing is a historical input written before policy-bound rehearsal.
	// Such artifacts remain verifiable; new runners bind their actual policy.
	if input.ProtocolDigest != "" && !characterSourceDigestPatternV2.MatchString(input.ProtocolDigest) {
		return input, fmt.Errorf("arc rehearsal has an invalid protocol digest")
	}
	if err := validateArcRehearsalExecutionCapabilitiesV1(input.ExecutionCapabilities); err != nil {
		return input, err
	}
	if input.ExecutionCapabilities != nil && input.ProtocolDigest == "" {
		return input, fmt.Errorf("execution capabilities require a policy-bound rehearsal input")
	}
	if len(input.Outline) != input.ArcLastChapter-input.ArcFirstChapter+1 || len(input.CharacterObservations) == 0 || len(input.HardContracts) == 0 || input.WorldState == nil {
		return input, fmt.Errorf("arc rehearsal input lacks complete arc slots, current characters, hard contracts or world state")
	}
	for i, chapter := range input.Outline {
		if chapter.Chapter != input.ArcFirstChapter+i {
			return input, fmt.Errorf("arc rehearsal outline must cover every ordered chapter")
		}
	}
	if err := ValidateWorldPhysicalStateV2(*input.WorldState); err != nil {
		return input, err
	}
	if len(input.SourceFiles) == 0 {
		return input, fmt.Errorf("rehearsal requires explicit frozen source-file hashes")
	}
	for name, digest := range input.SourceFiles {
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.HasPrefix(name, "meta/planning/arc_rehearsals") || !characterSourceDigestPatternV2.MatchString(digest) {
			return input, fmt.Errorf("invalid rehearsal source-file binding")
		}
	}
	seen := map[string]bool{}
	for _, observation := range input.CharacterObservations {
		valid, err := FinalizeCharacterObservationPacket(observation)
		if err != nil || !samePhysicalValueV2(valid, observation) || observation.Chapter != input.BaseCanonChapter+1 || seen[observation.Character] {
			return input, fmt.Errorf("rehearsal requires exact host current-character observations")
		}
		seen[observation.Character] = true
	}
	summaries := map[int]string{}
	for _, summary := range input.AcceptedSummaries {
		summaries[summary.Chapter] = summary.Summary
	}
	for chapter := input.ArcFirstChapter; chapter <= input.BaseCanonChapter; chapter++ {
		if summaries[chapter] == "" || !characterSourceDigestPatternV2.MatchString(input.AcceptedEvidence[chapter]) {
			return input, fmt.Errorf("rehearsal lacks accepted evidence for historical chapter %d", chapter)
		}
	}
	input.InputDigest = ""
	digest, err := DeterministicPlanningHash(input)
	if err != nil {
		return input, err
	}
	input.InputDigest = "sha256:" + digest
	return input, nil
}

func ValidateArcRehearsalBody(input ArcRehearsalInput, body ArcRehearsalBody) error {
	if err := validateArcRehearsalCapabilitiesV1(input, body); err != nil {
		return err
	}
	// Each rejection below names the exact offending entry and, where the host
	// owns the expected bytes, prints them. The previous single-sentence messages
	// ("...must preserve each current goal and mark choices conditional") covered
	// four different mistakes at once, so the Architect could not tell whether the
	// character name, the verbatim current_goal copy, conflicts or
	// conditional_choices was wrong and burned every MaxTurns retry (measured:
	// 4/4 turns rejected on the first arc rehearsal).
	if strings.TrimSpace(body.Summary) == "" {
		return fmt.Errorf("rehearsal requires a substantive conditional forecast for the whole arc: summary 不能为空")
	}
	if len(body.Chapters) != len(input.Outline) {
		return fmt.Errorf("rehearsal chapters 必须按顺序覆盖整弧全部章位：got=%d want=%d（本弧第 %d-%d 章）", len(body.Chapters), len(input.Outline), input.ArcFirstChapter, input.ArcFirstChapter+len(input.Outline)-1)
	}
	if len(body.MaterialChecks) == 0 {
		return fmt.Errorf("rehearsal requires explicit material checks: material_checks 不能为空")
	}
	for i, chapter := range body.Chapters {
		if chapter.Chapter != input.ArcFirstChapter+i {
			return fmt.Errorf("rehearsal chapters[%d].chapter=%d 必须等于 %d（按顺序覆盖整弧）", i, chapter.Chapter, input.ArcFirstChapter+i)
		}
		if strings.TrimSpace(chapter.ConditionalForecast) == "" {
			return fmt.Errorf("rehearsal chapter %d lacks conditional_forecast：已接受章逐字复制原摘要，未来章写条件预测，不能留空", chapter.Chapter)
		}
		if len(nonEmptyWorldStrings(chapter.Assumptions)) == 0 {
			return fmt.Errorf("rehearsal chapter %d lacks assumptions：列出条件假设，不得伪称已执行", chapter.Chapter)
		}
		if len(nonEmptyWorldStrings(chapter.CausalLinks)) == 0 {
			return fmt.Errorf("rehearsal chapter %d lacks causal_links：列出假设下的因果关系", chapter.Chapter)
		}
		if len(nonEmptyWorldStrings(chapter.TimeResourceChecks)) == 0 {
			return fmt.Errorf("rehearsal chapter %d lacks time_resource_checks：列出时间资源限制与未决条件", chapter.Chapter)
		}
		if chapter.Chapter <= input.BaseCanonChapter {
			for _, summary := range input.AcceptedSummaries {
				if summary.Chapter == chapter.Chapter && (chapter.AcceptedSourceDigest != input.AcceptedEvidence[chapter.Chapter] || chapter.ConditionalForecast != summary.Summary) {
					return fmt.Errorf("rehearsal cannot rewrite accepted chapter %d", chapter.Chapter)
				}
			}
		} else if chapter.AcceptedSourceDigest != "" {
			return fmt.Errorf("future rehearsal chapter cannot claim accepted evidence")
		}
	}
	characters := map[string]string{}
	observed := make([]string, 0, len(input.CharacterObservations))
	for _, o := range input.CharacterObservations {
		characters[o.Character] = o.CurrentGoal
		observed = append(observed, o.Character)
	}
	for _, c := range body.CharacterConflicts {
		goal, ok := characters[c.Character]
		if !ok {
			return fmt.Errorf("rehearsal character_conflicts 里的角色 %q 不在宿主当前观察名单中；角色名必须逐字一致，只能是 %s", c.Character, strings.Join(observed, "、"))
		}
		if c.CurrentGoal != goal {
			return fmt.Errorf("rehearsal character_conflicts[%q].current_goal 必须逐字复制宿主观察到的当前目标：expected=%q actual=%q", c.Character, goal, c.CurrentGoal)
		}
		if len(nonEmptyWorldStrings(c.Conflicts)) == 0 {
			return fmt.Errorf("rehearsal character_conflicts[%q].conflicts 不能为空：列出该角色的目标冲突", c.Character)
		}
		if len(nonEmptyWorldStrings(c.ConditionalChoices)) == 0 {
			return fmt.Errorf("rehearsal character_conflicts[%q].conditional_choices 不能为空：写条件性可能行为，不是正式决定", c.Character)
		}
		delete(characters, c.Character)
	}
	if len(characters) != 0 {
		var missing []string
		for _, name := range observed {
			if goal, ok := characters[name]; ok {
				missing = append(missing, fmt.Sprintf("%s（current_goal=%q）", name, goal))
			}
		}
		return fmt.Errorf("rehearsal omitted a current principal character：每个宿主观察角色都必须逐条出现在 character_conflicts 中，缺 %s", strings.Join(missing, "、"))
	}
	contracts := map[string]bool{}
	for _, text := range input.HardContracts {
		contracts[text] = true
	}
	for _, c := range body.ContractChecks {
		if !contracts[c.Contract] {
			return fmt.Errorf("rehearsal contract_checks 的 contract 字段必须逐字复制输入 hard_contracts 原文，%q 不在其中（本弧 hard_contracts 第一条为 %q）", c.Contract, outlineContractTextPrefix(input.HardContracts[0], 80))
		}
		if len(nonEmptyWorldStrings(c.Conditions)) == 0 {
			return fmt.Errorf("rehearsal contract_checks[%q].conditions 不能为空：列出判断所需条件与依据", outlineContractTextPrefix(c.Contract, 60))
		}
		switch c.Assessment {
		case "plausible", "conditional", "unresolved", "infeasible_prediction":
		default:
			return fmt.Errorf("invalid speculative contract assessment %q（可选 plausible/conditional/unresolved/infeasible_prediction）", c.Assessment)
		}
		delete(contracts, c.Contract)
	}
	if len(contracts) != 0 {
		var missing []string
		for _, text := range input.HardContracts {
			if contracts[text] {
				missing = append(missing, outlineContractTextPrefix(text, 60))
			}
		}
		return fmt.Errorf("rehearsal omitted a hard contract：每条 hard_contract 都必须出现在 contract_checks 中，缺 %s", strings.Join(missing, "；"))
	}
	resources := map[string]WorldResourceBalanceV2{}
	for _, r := range input.WorldState.Resources {
		resources[r.ResourceID] = r
	}
	for i, m := range body.MaterialChecks {
		if strings.TrimSpace(m.Operation) == "" {
			return fmt.Errorf("rehearsal material_checks[%d].operation 不能为空：写关键操作名", i)
		}
		if strings.TrimSpace(m.Explanation) == "" {
			return fmt.Errorf("rehearsal material_checks[%q].explanation 不能为空：写来源、可读事实与真实缺口", m.Operation)
		}
		switch m.Status {
		case "available", "missing", "unclear", "not_required":
		default:
			return fmt.Errorf("invalid rehearsal material status")
		}
		if m.RequiresReadable && (m.Status == "not_required" || (input.ExecutionCapabilities == nil && m.Status == "available" && len(m.ResourceRefs) == 0)) {
			return fmt.Errorf("read-dependent operation cannot claim availability without an existing readable resource")
		}
		for _, ref := range m.ResourceRefs {
			r, ok := resources[ref]
			if !ok {
				return fmt.Errorf("rehearsal cannot invent resource reference %q", ref)
			}
			if m.Status == "available" && m.RequiresReadable && len(r.ReadableFacts) == 0 && (input.ExecutionCapabilities == nil || r.Artifact == nil) {
				return fmt.Errorf("rehearsal resource %q has no readable facts", ref)
			}
		}
	}
	raw, _ := json.Marshal(body)
	if len(raw) > 256*1024 {
		return fmt.Errorf("arc rehearsal exceeds its bounded report size")
	}
	return nil
}

func validArcRehearsalCall(call ArcRehearsalCall, role string) bool {
	if call.Role != role || call.Provider == "" || call.Model == "" || len(call.UsageIDs) == 0 || call.ToolCallID == "" || !characterSourceDigestPatternV2.MatchString(call.ResponseDigest) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range call.UsageIDs {
		if id == "" || strings.TrimSpace(id) != id || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func FinalizeArcRehearsalDraft(input ArcRehearsalInput, draft ArcRehearsalDraft) (ArcRehearsalDraft, error) {
	if err := ValidateArcRehearsalBody(input, draft.Body); err != nil {
		return draft, err
	}
	if !validArcRehearsalCall(draft.Call, "architect") {
		return draft, fmt.Errorf("rehearsal draft lacks actual Architect response/usage sources")
	}
	draft.Version, draft.Authority, draft.InputDigest = ArcRehearsalVersion, "speculative", input.InputDigest
	draft.DraftDigest = ""
	digest, err := DeterministicPlanningHash(draft)
	draft.DraftDigest = "sha256:" + digest
	return draft, err
}

func FinalizeArcRehearsalReport(input ArcRehearsalInput, draft ArcRehearsalDraft, report ArcRehearsalReport) (ArcRehearsalReport, error) {
	valid, err := FinalizeArcRehearsalDraft(input, draft)
	if err != nil || !samePhysicalValueV2(valid, draft) {
		return report, fmt.Errorf("rehearsal review lacks its exact verified draft")
	}
	if err := ValidateArcRehearsalReviewBody(input, draft.Body, report.Body); err != nil {
		return report, err
	}
	if !validArcRehearsalCall(report.Call, "world_arbiter") {
		return report, fmt.Errorf("rehearsal review lacks actual Arbiter response/usage sources")
	}
	for _, draftID := range draft.Call.UsageIDs {
		for _, reviewID := range report.Call.UsageIDs {
			if draftID == reviewID {
				return report, fmt.Errorf("rehearsal draft and review cannot reuse one model-call usage identity")
			}
		}
	}
	report.Version, report.Authority = ArcRehearsalVersion, "speculative"
	report.ArcID, report.ArcFirstChapter, report.ArcLastChapter = input.ArcID, input.ArcFirstChapter, input.ArcLastChapter
	report.BaseCanonChapter, report.BaseCanonRoot, report.SourceRoot = input.BaseCanonChapter, input.BaseCanonRoot, input.SourceRoot
	report.InputDigest, report.DraftDigest = input.InputDigest, draft.DraftDigest
	report.ReadyForDetail = true
	// Only known-impossible outcomes block detailed planning.
	//
	// Contract `unresolved` and material `unclear` are decisively NOT blocking
	// here. Every arc's input carries the same whole-book hard contracts
	// (arc_rehearsal_input.go appends compass.EndingDirection plus all
	// non_negotiables), so a first arc must assess the finale, the mid-book
	// dao-heart collapse, the one-year madness period and the personality split —
	// all of which can only honestly be reported as "outside this arc"
	// (measured: 6 of 9 contract checks in arc 1-8, every one of them annotated
	// "弧1-8外, 远期…"). Treating that honesty as a blocker makes the first arc
	// unreachable for any book with book-level contracts, no matter how well the
	// arc itself is planned.
	//
	// A prediction the host cannot falsify is recorded in unresolved_items and
	// re-checked during detailed planning; `infeasible_prediction` is the verdict
	// for an arc path that cannot come true, and material `missing` is the verdict
	// for a resource the arc actually needs and does not have. Those two remain
	// hard blockers.
	for _, check := range report.Body.ContractChecks {
		if check.Assessment == "infeasible_prediction" {
			report.ReadyForDetail = false
		}
	}
	for _, check := range report.Body.MaterialChecks {
		if check.Status == "missing" {
			report.ReadyForDetail = false
		}
	}
	report.ReportDigest = ""
	digest, err := DeterministicPlanningHash(report)
	report.ReportDigest = "sha256:" + digest
	return report, err
}

// ValidateArcRehearsalReviewBody validates the full review and preserves every
// original dependency by key. Additions remain subject to the same typed/global
// checks; independent display order is not a new execution constraint. Callers
// authenticate the host-bound draft separately; this cannot create Call data.
func ValidateArcRehearsalReviewBody(input ArcRehearsalInput, draft, review ArcRehearsalBody) error {
	if err := ValidateArcRehearsalBody(input, review); err != nil {
		return err
	}
	for _, prior := range draft.MaterialChecks {
		found := false
		for _, current := range review.MaterialChecks {
			if current.Operation == prior.Operation {
				found = true
				if prior.RequiresReadable && !current.RequiresReadable {
					return fmt.Errorf("review cannot remove a declared readable-material dependency")
				}
				if input.ExecutionCapabilities != nil {
					for _, original := range prior.CapabilityRequirements {
						preserved := false
						for _, requirement := range current.CapabilityRequirements {
							if original.Key == requirement.Key && samePhysicalValueV2(original, requirement) {
								preserved = true
								break
							}
						}
						if !preserved {
							return fmt.Errorf("review cannot rewrite declared execution dependencies for %q", prior.Operation)
						}
					}
				}
				if input.ExecutionCapabilities != nil && prior.Status != "not_required" && current.Status == "not_required" {
					return fmt.Errorf("review cannot discard a selected material dependency")
				}
			}
		}
		if !found {
			return fmt.Errorf("review omitted draft material check %q", prior.Operation)
		}
	}
	return nil
}
