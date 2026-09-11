package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/voocel/agentcore"
)

type groundingProbeModel struct {
	args     string
	calls    int
	messages []agentcore.Message
	specs    []agentcore.ToolSpec
	// truncate models a reviewer that spent its whole output budget on provider
	// reasoning and therefore emitted no verdict tool call at all.
	truncate bool
	output   int
	// responses, when set, answers each successive call differently.
	responses []string
}

func (m *groundingProbeModel) Generate(_ context.Context, messages []agentcore.Message, specs []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.calls++
	m.messages = messages
	m.specs = specs
	if m.truncate {
		return &agentcore.LLMResponse{Message: agentcore.Message{
			Role:       agentcore.RoleAssistant,
			Content:    []agentcore.ContentBlock{agentcore.TextBlock("正在核对裁决与计划")},
			StopReason: agentcore.StopReasonLength,
			Usage:      &agentcore.Usage{Output: m.output},
		}}, nil
	}
	args := m.args
	if len(m.responses) > 0 {
		index := m.calls - 1
		if index >= len(m.responses) {
			index = len(m.responses) - 1
		}
		args = m.responses[index]
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(agentcore.ToolCall{ID: "verdict", Name: "submit_plan_grounding_verdict", Args: json.RawMessage(args)})}}}, nil
}
func (*groundingProbeModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	panic("grounding must use one non-streaming classification")
}
func (*groundingProbeModel) SupportsTools() bool { return true }

func TestPlanGroundingModelHasOneReadOnlyCapabilityAndExactSections(t *testing.T) {
	input := domain.PlanGroundingInput{Policy: domain.PlanGroundingPolicyV1, ReviewProtocol: "sha256:" + strings.Repeat("1", 64), Plan: domain.ChapterPlan{Chapter: 1, Goal: strings.Repeat("长", 20000) + "完整计划尾"}, POVObservation: domain.CharacterObservationPacket{CurrentGoal: strings.Repeat("知", 20000) + "完整观察尾"}}
	m := &groundingProbeModel{args: `{"pass":true,"findings":[]}`}
	if _, err := runPlanGroundingReview(context.Background(), m, agentcore.ThinkingHigh, input); err != nil {
		t.Fatal(err)
	}
	if m.calls != 1 || len(m.specs) != 1 || m.specs[0].Name != "submit_plan_grounding_verdict" {
		t.Fatal("unexpected capabilities/calls")
	}
	if len(m.messages) != 5 || !strings.Contains(m.messages[2].TextContent(), "完整观察尾") || !strings.Contains(m.messages[4].TextContent(), "完整计划尾") {
		t.Fatal("exact source was omitted or truncated")
	}
	input.Plan.Goal = strings.Repeat("大", 44001)
	if _, err := runPlanGroundingReview(context.Background(), m, agentcore.ThinkingHigh, input); err == nil {
		t.Fatal("oversized section silently truncated")
	}
	if m.calls != 1 {
		t.Fatal("over-budget evidence reached provider")
	}
}

// A verdict whose quotes are not verbatim excerpts is the reviewer's own defect.
// Routing it to the Planner (which cannot quote the reviewer's evidence) turns one
// bad excerpt into an unbounded paid planning loop, so the reviewer must be
// re-asked once, with the exact host rule it broke.
func TestUnverifiableReviewerExcerptIsRepairedByTheReviewer(t *testing.T) {
	input := domain.PlanGroundingInput{
		Policy:         domain.PlanGroundingPolicyV1,
		Plan:           domain.ChapterPlan{Chapter: 1, Goal: "沈大同在渡口清点灵石"},
		POVObservation: domain.CharacterObservationPacket{Character: "沈大同", CurrentGoal: "在渡口等试炼"},
	}
	ellipsis := `{"pass":false,"findings":[{"kind":"time","plan_path":"/plan/goal","plan_quote":"沈大同…清点灵石","source_path":"/pov_observation/current_goal","source_quote":"在渡口等试炼","explanation":"计划动作超出开章观察"}]}`
	verbatim := `{"pass":false,"findings":[{"kind":"time","plan_path":"/plan/goal","plan_quote":"沈大同在渡口清点灵石","source_path":"/pov_observation/current_goal","source_quote":"在渡口等试炼","explanation":"计划动作超出开章观察"}]}`
	repaired := &groundingProbeModel{responses: []string{ellipsis, verbatim}}
	verdict, err := runPlanGroundingReviewWithRepair(context.Background(), repaired, agentcore.ThinkingHigh, input, []agentcore.Message{agentcore.SystemMsg(planGroundingPrompt)}, planGroundingMaxOutputTokens)
	if err != nil || verdict.Pass || len(verdict.Findings) != 1 || repaired.calls != 2 {
		t.Fatalf("reviewer repair did not converge: calls=%d pass=%t findings=%d err=%v", repaired.calls, verdict.Pass, len(verdict.Findings), err)
	}
	feedback := repaired.messages[len(repaired.messages)-1].TextContent()
	if !strings.Contains(feedback, "unverifiable excerpt") || !strings.Contains(feedback, "逐字") || !strings.Contains(feedback, "findings") {
		t.Fatalf("repair feedback did not state the broken host rule: %s", feedback)
	}
	stuck := &groundingProbeModel{responses: []string{ellipsis}}
	if _, err := runPlanGroundingReviewWithRepair(context.Background(), stuck, agentcore.ThinkingHigh, input, []agentcore.Message{agentcore.SystemMsg(planGroundingPrompt)}, planGroundingMaxOutputTokens); err == nil || !strings.Contains(err.Error(), "failed host verification after 1 repair attempt") {
		t.Fatalf("repeated unverifiable verdict did not stop after one repair: %v", err)
	}
	if stuck.calls != 2 {
		t.Fatalf("repair attempts were not bounded: calls=%d", stuck.calls)
	}
}

func TestPlanGroundingRejectsMalformedAndFabricatedVerdicts(t *testing.T) {
	input := domain.PlanGroundingInput{Policy: domain.PlanGroundingPolicyV1, Plan: domain.ChapterPlan{Goal: "未读材料"}}
	for _, args := range []string{`{}`, `{"pass":true}`, `{"pass":true,"findings":null}`, `{"findings":[]}`, `{"pass":false,"findings":[]}`, `{"pass":true,"findings":[],"thoughts":"private reasoning"}`, `{"pass":true,"findings":[]} {}`, `{"pass":false,"findings":[{"kind":"knowledge","plan_path":"/plan/goal","plan_quote":"未读材料","source_path":"/arbitration/resolutions/9/state_after","source_quote":"编造来源","explanation":"假矛盾"}]}`} {
		m := &groundingProbeModel{args: args}
		if _, err := runPlanGroundingReview(context.Background(), m, agentcore.ThinkingHigh, input); err == nil {
			t.Fatalf("accepted malformed verdict: %s", args)
		}
	}
}
