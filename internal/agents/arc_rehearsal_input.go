package agents

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhongyang/novel-studio/internal/bootstrap"
	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/chenhongyang/novel-studio/internal/rules"
	"github.com/chenhongyang/novel-studio/internal/store"
)

// BuildArcRehearsalInput only reads authored foundation and accepted state.
// It never reads preplan projections, draft plans or its own rehearsal files.
// splitArcRehearsalContracts separates this arc's own payoff obligations from the
// whole-book contracts that stay background. An arc owes the contracts its own
// chapters must pay off (the chapter-level contract refs map_contracts froze);
// the finale and the other long non-negotiables belong to whichever arc actually
// pays them off.
func splitArcRehearsalContracts(compass domain.StoryCompass, outline []domain.OutlineEntry) (arcContracts, bookContracts []string) {
	bookContracts = compactAgentStrings(append([]string{compass.EndingDirection}, compass.NonNegotiables...))
	seen := map[string]bool{}
	arcContracts = make([]string, 0, len(outline))
	for _, entry := range outline {
		for _, ref := range entry.ContractRefs {
			text := strings.TrimSpace(ref.PlannedResolution)
			if text == "" || seen[text] {
				continue
			}
			seen[text] = true
			arcContracts = append(arcContracts, fmt.Sprintf("%s（本弧第 %d 章须兑现）", text, entry.Chapter))
		}
	}
	if len(arcContracts) == 0 {
		// The validator requires at least one hard contract, and an arc still owes
		// the local rules it can violate on its own (no early payoff of another
		// arc's contract, no skipped foreshadowing).
		arcContracts = append(arcContracts, "本弧不承担任何指南针合同的兑现点（全部合同由其他弧兑现）：不得提前消费后续弧的合同兑现，也不得让本弧角色越过自己的知识边界预支后续信息")
	}
	return compactAgentStrings(arcContracts), bookContracts
}

func BuildArcRehearsalInput(st *store.Store, binding domain.ArcRehearsalInput, configs ...bootstrap.Config) (domain.ArcRehearsalInput, error) {
	input := domain.ArcRehearsalInput{Version: domain.ArcRehearsalVersion, ArcID: binding.ArcID, ArcFirstChapter: binding.ArcFirstChapter, ArcLastChapter: binding.ArcLastChapter, BaseCanonChapter: binding.BaseCanonChapter, BaseCanonRoot: binding.BaseCanonRoot, SourceRoot: binding.SourceRoot, SourceFiles: map[string]string{}}
	if st == nil {
		return input, fmt.Errorf("rehearsal requires a source Store")
	}
	protocol, err := ArcRehearsalProtocolDigest()
	if err != nil {
		return input, err
	}
	input.ProtocolDigest = protocol
	if len(configs) > 1 {
		return input, fmt.Errorf("rehearsal accepts one actual execution configuration")
	}
	// Omission is only the standalone/default API path. Production CLI callers
	// pass their resolved config; Run independently checks the same selection.
	var cfg bootstrap.Config
	if len(configs) == 1 {
		cfg = configs[0]
	}
	capabilities, err := ArcRehearsalExecutionCapabilities(cfg)
	if err != nil {
		return input, err
	}
	input.ExecutionCapabilities = &capabilities
	read := func(rel string, out any) error {
		raw, err := os.ReadFile(filepath.Join(st.Dir(), rel))
		if err != nil {
			return fmt.Errorf("read rehearsal source %s: %w", rel, err)
		}
		input.SourceFiles[rel] = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode rehearsal source %s: %w", rel, err)
		}
		return nil
	}
	var characters []domain.Character
	var outline []domain.OutlineEntry
	var compass domain.StoryCompass
	var userRules rules.Snapshot
	for _, source := range []struct {
		path string
		out  any
	}{{"characters.json", &characters}, {"outline.json", &outline}, {"world_rules.json", &input.WorldRules}, {"world_codex.json", &input.WorldCodex}, {"book_world.json", &input.BookWorld}, {"meta/compass.json", &compass}, {"meta/user_rules.json", &userRules}} {
		if err := read(source.path, source.out); err != nil {
			return input, err
		}
	}
	if successor, err := st.CharacterAgents.LoadCurrentSuccessorPlan(); err != nil {
		return input, err
	} else if successor != nil && successor.BaseCanonChapter == input.BaseCanonChapter && successor.ArcFirstChapter == input.ArcFirstChapter && successor.ArcLastChapter == input.ArcLastChapter {
		// successor.accepted_canon_root was stamped while project-all was running
		// inside its own workspace store, and this rehearsal reads the live store.
		// The same root function therefore describes two different staged states
		// for the same canon, so the two values can never be compared directly:
		// a legitimate successor was rejected as "foreign" and the arc could not
		// be rehearsed again. Bind the successor by its registered current
		// identity and require the live canon to still stand exactly where the
		// successor was accepted — a store-independent property that the root
		// comparison was only approximating.
		progress, err := st.Progress.Load()
		if err != nil {
			return input, err
		}
		if progress == nil {
			return input, fmt.Errorf("rehearsal successor requires readable live progress")
		}
		if progress.CurrentChapter != successor.BaseCanonChapter {
			return input, fmt.Errorf("rehearsal successor was accepted at canon chapter %d but the live canon is at chapter %d", successor.BaseCanonChapter, progress.CurrentChapter)
		}
		if err := read("meta/character_agents/successors/current.json", nil); err != nil {
			return input, err
		}
		if err := read(filepath.ToSlash(filepath.Join("meta/character_agents/successors", successor.ParentGenerationID, strings.TrimPrefix(successor.Digest, "sha256:")+".json")), nil); err != nil {
			return input, err
		}
		for i := range outline {
			for _, revised := range successor.RevisedChapters {
				if revised.Chapter == outline[i].Chapter {
					outline[i] = revised
				}
			}
		}
	}
	for _, entry := range outline {
		if entry.Chapter >= input.ArcFirstChapter && entry.Chapter <= input.ArcLastChapter {
			input.Outline = append(input.Outline, entry)
		}
	}
	sort.Slice(input.Outline, func(i, j int) bool { return input.Outline[i].Chapter < input.Outline[j].Chapter })
	// Hard contracts are ARC-SCOPED. Feeding every arc the same whole-book list
	// (ending direction plus all non-negotiables) made an honest first arc report
	// the finale as unreachable — `unresolved` first, then `infeasible_prediction`
	// once the rejection text pushed for definite verdicts — so the readiness gate
	// could only block every book forever or be relaxed, and relaxing it silently
	// let detailed planning proceed with unresolved finale contracts. The arc owes
	// the contracts its own chapters must pay off; the whole-book set stays visible
	// through ForwardContracts for foreshadowing only.
	arcContracts, bookContracts := splitArcRehearsalContracts(compass, input.Outline)
	input.HardContracts = arcContracts
	input.ForwardContracts = bookContracts
	if userRules.Status != rules.StatusReady {
		return input, fmt.Errorf("arc rehearsal requires ready normalized user rules")
	}
	input.UserRules, _ = json.Marshal(map[string]any{"structured": userRules.Structured, "preferences": userRules.Preferences})
	if words := userRules.Structured.ChapterWords; words != nil {
		input.HardContracts = append(input.HardContracts, fmt.Sprintf("每章正文%d—%d字（meta/user_rules.json:structured.chapter_words）", words.Min, words.Max))
	}
	registry := domain.CharacterAgentRegistry{Version: domain.CharacterAgentRegistryVersion}
	if input.BaseCanonChapter > 0 {
		stored, err := st.CharacterAgents.LoadRegistry()
		if err != nil {
			return input, err
		}
		if stored == nil {
			return input, fmt.Errorf("accepted rehearsal baseline lacks its character registry")
		}
		registry = *stored
	} else {
		for _, character := range characters {
			if character.InitialState == nil {
				continue
			}
			updated, _, err := registry.UpsertCharacter(character.Name, character.Aliases, character.Tier, 0, "")
			if err != nil {
				return input, err
			}
			registry = updated
		}
	}
	currentChoices := map[string]domain.CharacterDecisionProposal{}
	if input.BaseCanonChapter == 0 {
		state, err := domain.BuildWorldPhysicalStateFromInitialV2(characters, registry)
		if err != nil {
			return input, err
		}
		input.WorldState = &state
	} else {
		bundle, err := st.LoadAcceptedCharacterAgentBundle(input.BaseCanonChapter)
		if err != nil {
			return input, err
		}
		input.WorldState = bundle.ChapterWorldSimulation.PhysicalState
		if bundle.CharacterActivationEvidence != nil {
			for _, cycle := range bundle.CharacterActivationEvidence.Cycles {
				for _, p := range domain.LatestCharacterCycleProposals(cycle.Evidence) {
					currentChoices[p.AgentID] = p
				}
			}
		} else if bundle.CharacterAgentEvidence != nil {
			for _, p := range domain.LatestCharacterCycleProposals(*bundle.CharacterAgentEvidence) {
				currentChoices[p.AgentID] = p
			}
		}
		input.AcceptedEvidence = map[int]string{}
		for chapter := input.ArcFirstChapter; chapter <= input.BaseCanonChapter; chapter++ {
			var summary domain.ChapterSummary
			if err := read(fmt.Sprintf("summaries/%02d.json", chapter), &summary); err != nil {
				return input, err
			}
			bodyPath := fmt.Sprintf("chapters/%02d.md", chapter)
			if err := read(bodyPath, nil); err != nil {
				return input, err
			}
			input.AcceptedSummaries = append(input.AcceptedSummaries, summary)
			input.AcceptedEvidence[chapter] = input.SourceFiles[bodyPath]
		}
	}
	for _, character := range characters {
		if character.Tier == "decorative" || character.InitialState == nil {
			continue
		}
		record, ok := registry.Resolve(character.Name)
		if !ok {
			return input, fmt.Errorf("rehearsal character lacks current identity: %s", character.Name)
		}
		var current *domain.CharacterPhysicalStateV2
		for i := range input.WorldState.Actors {
			if input.WorldState.Actors[i].AgentID == record.AgentID {
				current = &input.WorldState.Actors[i]
			}
		}
		if current == nil {
			return input, fmt.Errorf("rehearsal character lacks accepted physical state: %s", character.Name)
		}
		var observationSources []string
		if input.ExecutionCapabilities.Policy == domain.ArcRehearsalCapabilityPolicyV2 {
			observationSources = []string{domain.CharacterSurfaceInspectionPolicyV1}
		}
		views, err := domain.BuildCharacterResourceViewsForSourcesV2(*input.WorldState, record.AgentID, observationSources)
		if err != nil {
			return input, err
		}
		o := domain.CharacterObservationPacket{Version: domain.CharacterObservationV2Version, GenerationID: "arc_rehearsal", Chapter: input.BaseCanonChapter + 1, Round: 1, AgentID: record.AgentID, Character: record.Character, Location: current.Location, CurrentGoal: character.InitialState.CurrentGoal, Pressure: character.InitialState.Pressure, StimulusDigest: input.SourceRoot, ResourceViews: views, Sources: observationSources}
		o.KnownFacts = []domain.CharacterAgentFact{newCharacterAgentFact("self_profile", "身份："+character.Name+"；岗位："+character.Role, "characters.json", "private")}
		if input.BaseCanonChapter == 0 {
			for _, fact := range character.InitialState.KnownFacts {
				o.KnownFacts = append(o.KnownFacts, newCharacterAgentFact("initial_known", fact, "characters.json#initial_state", "private"))
			}
		} else {
			var memory domain.CharacterAgentMemory
			if err := read("meta/character_agents/memory/"+record.AgentID+".json", &memory); err != nil {
				return input, err
			}
			valid, err := domain.FinalizeCharacterAgentMemory(memory)
			if err != nil || valid.MemoryRoot != memory.MemoryRoot || memory.State != "canonical" || memory.LastAcceptedChapter > input.BaseCanonChapter {
				return input, fmt.Errorf("rehearsal lacks verified accepted memory for %s", character.Name)
			}
			o.Memory, o.MemoryRoot = memory.Facts, memory.MemoryRoot
			if p, ok := currentChoices[record.AgentID]; ok {
				o.CurrentGoal, o.Pressure = p.CurrentGoal, p.Pressure
			}
		}
		for _, rule := range input.WorldRules {
			if view := strings.TrimSpace(rule.CharacterView); view != "" && domain.WorldRuleVisibility(rule) != "secret" {
				o.PublicRules = append(o.PublicRules, newCharacterAgentFact("world_rule_view", view, "world_rules.json", domain.WorldRuleVisibility(rule)))
			}
		}
		if input.WorldCodex != nil {
			for _, mechanism := range input.WorldCodex.Mechanisms {
				if visible, ok := characterFacingMechanism(mechanism); ok {
					o.PublicMechanisms = append(o.PublicMechanisms, visible)
				}
			}
		}
		o, err = domain.FinalizeCharacterObservationPacket(o)
		if err != nil {
			return input, err
		}
		input.CharacterObservations = append(input.CharacterObservations, o)
	}
	sort.Slice(input.CharacterObservations, func(i, j int) bool {
		return input.CharacterObservations[i].AgentID < input.CharacterObservations[j].AgentID
	})
	return domain.FinalizeArcRehearsalInput(input)
}

func ArcRehearsalExecutionCapabilities(cfg bootstrap.Config) (domain.ArcRehearsalExecutionCapabilitiesV1, error) {
	policy := cfg.CharacterActivationPolicy()
	producer := CharacterAgentProtocolDigestForVersion(cfg.CharacterAgentsProtocolVersion())
	if policy != "" {
		producer = CharacterActivationProtocolWithProducer(policy, cfg.CharacterAgents.FrozenActivationProducer)
	} else if cfg.CharacterAgents.FrozenActivationProducer != "" {
		return domain.ArcRehearsalExecutionCapabilitiesV1{}, fmt.Errorf("one-shot rehearsal cannot claim a frozen activation producer")
	}
	if cfg.CharacterAgents.ExecutionPolicy != "" && cfg.CharacterAgents.ExecutionPolicy != "v1" && policy == "" {
		return domain.ArcRehearsalExecutionCapabilitiesV1{}, fmt.Errorf("rehearsal execution selection is not a valid activation configuration")
	}
	if policy == domain.CharacterActivationCyclePolicyV3 && domain.HasCharacterSurfaceInspectionPolicyV1(characterActivationV3PoliciesForProducer(producer)) {
		return domain.BuildArcRehearsalExecutionCapabilitiesV2(cfg.CharacterAgentsProtocolVersion(), policy, producer)
	}
	return domain.BuildArcRehearsalExecutionCapabilitiesV1(cfg.CharacterAgentsProtocolVersion(), policy, producer)
}
