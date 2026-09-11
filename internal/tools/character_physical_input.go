package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/chenhongyang/novel-studio/internal/domain"
)

type arbitrationResolutionInput struct {
	domain.CharacterDecisionResolution
	PostState json.RawMessage `json:"post_state"`
}

type characterPhysicalPostStateInput struct {
	AgentID         *string         `json:"agent_id"`
	Character       *string         `json:"character"`
	Location        string          `json:"location"`
	Resources       json.RawMessage `json:"resources"`
	ResourceUpdates json.RawMessage `json:"resource_updates"`
	ReceivedFacts   json.RawMessage `json:"received_facts"`
}

type characterResourceUpdateInput struct {
	ResourceID     string                       `json:"resource_id"`
	PerceivedName  *string                      `json:"perceived_name"`
	PerceivedLabel *string                      `json:"perceived_label"`
	PerceivedUnit  *string                      `json:"perceived_unit"`
	Access         *string                      `json:"access"`
	Perception     *domain.ResourcePerceptionV2 `json:"perception"`
	EvidenceRefs   *[]string                    `json:"evidence_refs"`
}

// The complete input still cannot author host-derived placement/progress.
type characterSelfHoldingInput struct {
	ResourceID     string                      `json:"resource_id"`
	PerceivedName  *string                     `json:"perceived_name"`
	PerceivedLabel *string                     `json:"perceived_label"`
	PerceivedUnit  *string                     `json:"perceived_unit"`
	Access         string                      `json:"access"`
	Perception     domain.ResourcePerceptionV2 `json:"perception"`
	EvidenceRefs   []string                    `json:"evidence_refs"`
}

func decodePhysicalInput(raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("physical input must be an explicit object/array, not null")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("physical input has trailing JSON")
	}
	return nil
}

func normalizeArbitrationResolutions(inputs []arbitrationResolutionInput, stimulus domain.WorldStimulusPacket, proposals []domain.CharacterDecisionProposal) ([]domain.CharacterDecisionResolution, error) {
	result := make([]domain.CharacterDecisionResolution, 0, len(inputs))
	byAgent := map[string]domain.CharacterDecisionProposal{}
	for _, proposal := range proposals {
		byAgent[proposal.AgentID] = proposal
	}
	for _, input := range inputs {
		resolution := input.CharacterDecisionResolution
		if stimulus.Version != domain.WorldStimulusPacketV2Version {
			if len(input.PostState) > 0 {
				return nil, fmt.Errorf("post_state requires v2 arbitration")
			}
			result = append(result, resolution)
			continue
		}
		proposal, ok := byAgent[resolution.AgentID]
		if !ok {
			return nil, fmt.Errorf("post_state has no bound proposal for %s", resolution.AgentID)
		}
		if resolution.Character != "" && resolution.Character != proposal.Character {
			return nil, fmt.Errorf("resolution character does not match bound actor")
		}
		resolution.Character = proposal.Character
		// The decision and its intent belong to the character agent, not to the
		// arbiter: an arbitration that paraphrases them rewrites another actor's
		// choice instead of reconciling the world. The host therefore owns these two
		// fields exactly as it owns Character above and substitutes the bound
		// proposal's text (an arbiter that genuinely disagrees must say so through
		// Conflicts, which re-opens a revision round).
		//
		// Rejecting instead made the whole project-all stage fail on chapter 1:
		// every retry re-emits the entire receipt, so the arbiter repaired one
		// character per turn ("arbiter rewrote intent ... intended_action must
		// exactly match original proposal.intended_action") until MaxTurns.
		if resolution.Decision != proposal.Decision || resolution.IntendedAction != proposal.IntendedAction {
			fmt.Fprintf(os.Stderr,
				"[world-arbitration] 宿主以提案原文覆盖被改写的 decision/intended_action： agent=%s character=%s\n",
				proposal.AgentID, proposal.Character)
			resolution.Decision = proposal.Decision
			resolution.IntendedAction = proposal.IntendedAction
		}
		// A null/absent post_state means "this actor's physical state is unchanged":
		// the arbiter emits it whenever a resolution carries no physical delta, and
		// treating it as a malformed input cost a whole arbitration resubmission per
		// attempt (measured on chapter 1 of project-all).
		if len(bytes.TrimSpace(input.PostState)) == 0 || bytes.Equal(bytes.TrimSpace(input.PostState), []byte("null")) {
			result = append(result, resolution)
			continue
		}
		var postInput characterPhysicalPostStateInput
		if err := decodePhysicalInput(input.PostState, &postInput); err != nil {
			return nil, fmt.Errorf("post_state %s: %w", resolution.AgentID, err)
		}
		if postInput.AgentID != nil && *postInput.AgentID != proposal.AgentID || postInput.Character != nil && *postInput.Character != proposal.Character {
			return nil, fmt.Errorf("post_state cannot replace its bound actor identity")
		}
		if stimulus.PhysicalState == nil {
			return nil, fmt.Errorf("post_state requires a validated physical baseline")
		}
		var before *domain.CharacterPhysicalStateV2
		for i := range stimulus.PhysicalState.Actors {
			actor := &stimulus.PhysicalState.Actors[i]
			if actor.AgentID == proposal.AgentID {
				before = actor
				break
			}
		}
		if before == nil {
			return nil, fmt.Errorf("post_state has no actor in physical baseline")
		}
		if len(postInput.Resources) > 0 && len(postInput.ResourceUpdates) > 0 {
			return nil, fmt.Errorf("post_state.resources and resource_updates are mutually exclusive")
		}
		var post domain.CharacterPhysicalStateV2
		raw, _ := json.Marshal(before)
		if err := decodePhysicalInput(raw, &post); err != nil {
			return nil, err
		}
		post.AgentID, post.Character, post.Location = proposal.AgentID, proposal.Character, postInput.Location
		if len(postInput.Resources) > 0 {
			if domain.HasCharacterSelfExperiencePolicyV2(stimulus.Sources) {
				var holdings []characterSelfHoldingInput
				if err := decodePhysicalInput(postInput.Resources, &holdings); err != nil {
					return nil, err
				}
				post.Resources = make([]domain.CharacterResourceHoldingV2, 0, len(holdings))
				for _, input := range holdings {
					holding := domain.CharacterResourceHoldingV2{ResourceID: input.ResourceID, Access: input.Access, Perception: input.Perception, EvidenceRefs: input.EvidenceRefs}
					for _, prior := range before.Resources {
						if prior.ResourceID != input.ResourceID {
							continue
						}
						holding.PerceivedName, holding.PerceivedLabel, holding.PerceivedUnit = prior.PerceivedName, prior.PerceivedLabel, prior.PerceivedUnit
						if prior.KnownPlacement != nil {
							copy := *prior.KnownPlacement
							holding.KnownPlacement = &copy
						}
					}
					if input.PerceivedName != nil {
						holding.PerceivedName = *input.PerceivedName
					}
					if input.PerceivedLabel != nil {
						holding.PerceivedLabel = *input.PerceivedLabel
					}
					if input.PerceivedUnit != nil {
						holding.PerceivedUnit = *input.PerceivedUnit
					}
					post.Resources = append(post.Resources, holding)
				}
			} else if err := decodePhysicalInput(postInput.Resources, &post.Resources); err != nil {
				return nil, err
			}
		} else if len(postInput.ResourceUpdates) > 0 {
			var updates []characterResourceUpdateInput
			if err := decodePhysicalInput(postInput.ResourceUpdates, &updates); err != nil {
				return nil, err
			}
			catalog := map[string]bool{}
			for _, resource := range stimulus.PhysicalState.Resources {
				catalog[resource.ResourceID] = true
			}
			seen := map[string]bool{}
			for _, update := range updates {
				if !catalog[update.ResourceID] || seen[update.ResourceID] {
					return nil, fmt.Errorf("resource_updates requires unique existing world resource_id: %s", update.ResourceID)
				}
				seen[update.ResourceID] = true
				index := -1
				for i := range post.Resources {
					if post.Resources[i].ResourceID == update.ResourceID {
						index = i
						break
					}
				}
				if index < 0 {
					post.Resources = append(post.Resources, domain.CharacterResourceHoldingV2{ResourceID: update.ResourceID, Access: "none", Perception: domain.ResourcePerceptionV2{Kind: "unaware"}})
					index = len(post.Resources) - 1
				}
				holding := &post.Resources[index]
				if update.PerceivedName != nil {
					holding.PerceivedName = *update.PerceivedName
				}
				if update.PerceivedLabel != nil {
					if !domain.HasCharacterSelfExperiencePolicyV2(stimulus.Sources) {
						return nil, fmt.Errorf("perceived_label updates require the self-experience policy")
					}
					holding.PerceivedLabel = *update.PerceivedLabel
				}
				if update.PerceivedUnit != nil {
					holding.PerceivedUnit = *update.PerceivedUnit
				}
				if update.Access != nil {
					holding.Access = *update.Access
				}
				if update.Perception != nil {
					holding.Perception = *update.Perception
				}
				if update.EvidenceRefs != nil {
					holding.EvidenceRefs = append([]string(nil), (*update.EvidenceRefs)...)
				}
			}
		}
		if post.Resources == nil {
			post.Resources = []domain.CharacterResourceHoldingV2{}
		}
		// received_facts is an append-only delivery patch at the tool boundary.
		// The complete authority stored by domain retains all prior known facts.
		if len(postInput.ReceivedFacts) > 0 {
			var addedFacts []domain.CharacterReceivedFactV2
			if err := decodePhysicalInput(postInput.ReceivedFacts, &addedFacts); err != nil {
				return nil, err
			}
			additions := make([]json.RawMessage, 0, len(addedFacts))
			for _, fact := range addedFacts {
				filled, err := fillReceivedFactFromSource(fact, proposal, stimulus, byAgent)
				if err != nil {
					return nil, err
				}
				encoded, _ := json.Marshal(filled)
				additions = append(additions, encoded)
			}
			raw, _ := json.Marshal(post)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			var existing []json.RawMessage
			if value := fields["received_facts"]; len(value) > 0 {
				if err := json.Unmarshal(value, &existing); err != nil {
					return nil, err
				}
			}
			existing = append(existing, additions...)
			fields["received_facts"], _ = json.Marshal(existing)
			raw, _ = json.Marshal(fields)
			if err := decodePhysicalInput(raw, &post); err != nil {
				return nil, err
			}
		}
		resolution.PostState = &post
		result = append(result, resolution)
	}
	return result, nil
}

// This only materializes an explicit received/read claim from immutable source
// content. Domain then verifies actual delivery/access/conditional read intent
// before any receipt can be persisted; it never treats an intention as delivery.
func fillReceivedFactFromSource(fact domain.CharacterReceivedFactV2, owner domain.CharacterDecisionProposal, stimulus domain.WorldStimulusPacket, proposals map[string]domain.CharacterDecisionProposal) (domain.CharacterReceivedFactV2, error) {
	if fact.Chapter != 0 && fact.Chapter != stimulus.Chapter {
		return fact, fmt.Errorf("received fact cannot replace bound chapter")
	}
	fact.Chapter = stimulus.Chapter
	var kind, text, digest string
	switch fact.SourceType {
	case "communication":
		sender, ok := proposals[fact.FromAgentID]
		if !ok {
			return fact, fmt.Errorf("received communication lacks a bound sender")
		}
		digest = sender.Digest
		for _, communication := range sender.Communications {
			if communication.ID == fact.SourceID && communication.ToCharacter == owner.Character {
				kind, text = communication.Kind, communication.Text
				break
			}
		}
	case "resource_read":
		digest = owner.Digest
		kind = "document_statement"
		for _, resource := range stimulus.PhysicalState.Resources {
			if resource.ResourceID != fact.ResourceID {
				continue
			}
			for _, source := range resource.ReadableFacts {
				if source.ID == fact.SourceID {
					text = source.Text
					break
				}
			}
		}
	default:
		return fact, fmt.Errorf("unsupported received fact source")
	}
	if text == "" || kind == "" {
		return fact, fmt.Errorf("received fact source is not present in the bound communication/document")
	}
	// The host owns kind/text/SourceProposalDigest: they are derived from the bound
	// communication or readable document just above, and the very next assignment
	// overwrites whatever the model sent. Rejecting a paraphrase here fails a
	// submission whose immutable content the host restores anyway (measured: two
	// rejections on chapter 1 of project-all). The binding checks stay — the source
	// must still exist and belong to this owner — but a differing echo is recorded,
	// not punished.
	if fact.SourceProposalDigest != "" && fact.SourceProposalDigest != digest ||
		fact.Kind != "" && fact.Kind != kind || fact.Text != "" && fact.Text != text {
		fmt.Fprintf(os.Stderr,
			"[world-arbitration] 宿主以绑定原文覆盖 received_fact 的 kind/text/digest： agent=%s source=%s\n",
			owner.AgentID, fact.SourceID)
	}
	fact.SourceProposalDigest, fact.Kind, fact.Text = digest, kind, text
	if fact.ID == "" {
		fact.ID = domain.CharacterReceivedFactIDV2(owner.AgentID, fact)
	}
	return fact, nil
}
