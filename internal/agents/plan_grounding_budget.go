package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/chenhongyang/novel-studio/internal/bootstrap"
	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/voocel/agentcore"
)

// This bounds the local activation serialization, not provider tokens. The
// Codex adapter independently bounds the complete system/tools/input/output
// schema transport and reserves output space against its operating window.
const planGroundingExactInputByteLimit = 2 * 1024 * 1024

// planGroundingHistoricalRuneCeiling was the fixed ceiling for every non-Codex
// provider. It assumed a roughly 64K-token window and therefore dead-ended real
// chapter planning on providers that serve far more. It stays the floor of every
// derived ceiling: no provider ever loses budget because of this file.
const planGroundingHistoricalRuneCeiling = 82000

// planGroundingAbsoluteRuneCeiling bounds one exact packet even when the
// reviewer window is far larger. A chapter plan for a few thousand words has no
// legitimate reason to reach this size, so this stays a hard local stop that
// reports a runaway planner instead of hiding it.
const planGroundingAbsoluteRuneCeiling = 400000

// planGroundingSectionRuneCeiling keeps every section below the Codex adapter's
// single-message budget; a derived window may widen it, never narrow it.
const planGroundingSectionRuneCeiling = 44000

// planGroundingWindowReserveTokens reserves room for the reviewer system prompt,
// the tool schema, the verdict and tokenizer slack.
const planGroundingWindowReserveTokens = 32768

// planGroundingRunesPerToken is a deliberately pessimistic CJK factor: one rune
// of Chinese prose can cost more than one provider token, so the rune budget is
// half the usable token window.
const planGroundingRunesPerToken = 2

// planGroundingRuneCeiling derives the exact-packet rune budget from the
// resolved context window of the model that will actually serve the verdict.
// Zero means "keep the historical ceiling", so an unresolved window can never
// widen evidence transport.
func planGroundingRuneCeiling(window int) int {
	if window <= 0 {
		return 0
	}
	budget := (window - planGroundingWindowReserveTokens) / planGroundingRunesPerToken
	if budget > planGroundingAbsoluteRuneCeiling {
		budget = planGroundingAbsoluteRuneCeiling
	}
	if budget <= planGroundingHistoricalRuneCeiling {
		return 0
	}
	return budget
}

// planGroundingRuneCeilingFor resolves the reviewer model's window exactly as
// the rest of the engine resolves it: config context_windows → bundled registry
// → engine default. Only a window that is actually known widens the packet: the
// engine default is a compaction fallback, not evidence about this provider, so
// it keeps the historical ceiling.
func planGroundingRuneCeilingFor(cfg bootstrap.Config, modelName string) int {
	if strings.TrimSpace(modelName) == "" {
		modelName = cfg.Roles["world_arbiter"].Model
	}
	window, source := cfg.ResolveContextWindow(modelName)
	if source == bootstrap.CtxWindowDefault {
		return 0
	}
	return planGroundingRuneCeiling(window)
}

func effectivePlanGroundingRuneCeiling(derived int) int {
	if derived <= 0 {
		return planGroundingHistoricalRuneCeiling
	}
	return derived
}

func planGroundingSectionBudget(derived int) int {
	if derived > planGroundingSectionRuneCeiling {
		return derived
	}
	return planGroundingSectionRuneCeiling
}

// planGroundingSection is one transport section: each keeps its original JSON
// key so a verdict's findings cite the same input, and each carries a log name.
type planGroundingSection struct {
	name  string
	value any
}

// planGroundingExactSections is the single definition of the evidence packet
// split. Review transport and capacity diagnostics must never disagree about it.
func planGroundingExactSections(input domain.PlanGroundingInput) []planGroundingSection {
	return []planGroundingSection{
		{"simulation", map[string]any{"policy": input.Policy, "review_protocol": input.ReviewProtocol, "simulation": input.Simulation}},
		{"pov_observation", map[string]any{"pov_observation": input.POVObservation}},
		{"arbitration", map[string]any{"arbitration": input.Arbitration}},
		{"plan", map[string]any{"plan": input.Plan}},
	}
}

// planGroundingInputScaleAttrs measures the exact packet and its dominant
// sections. A capacity stop must be diagnosable from the run log alone, without
// replaying the paid planning session that produced the packet.
func planGroundingInputScaleAttrs(input domain.PlanGroundingInput, total, ceiling int) []any {
	attrs := []any{"total_runes", total, "ceiling_runes", ceiling}
	if input.Activation != nil {
		return append(attrs, "activation_trace", true)
	}
	for _, section := range planGroundingExactSections(input) {
		raw, err := json.Marshal(section.value)
		if err != nil {
			continue
		}
		attrs = append(attrs, section.name+"_runes", utf8.RuneCount(raw))
		if section.name == "plan" {
			attrs = append(attrs, "plan_largest_fields", planGroundingLargestPlanFields(input.Plan))
		}
	}
	return attrs
}

// planGroundingLargestPlanFields names the fields that dominate a plan section so
// an oversized plan points at the fields a planner must compress.
func planGroundingLargestPlanFields(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ""
	}
	type sized struct {
		name  string
		runes int
	}
	sizes := make([]sized, 0, len(fields))
	for name, field := range fields {
		sizes = append(sizes, sized{name: name, runes: utf8.RuneCount(field)})
	}
	for i := 1; i < len(sizes); i++ {
		for j := i; j > 0 && sizes[j].runes > sizes[j-1].runes; j-- {
			sizes[j], sizes[j-1] = sizes[j-1], sizes[j]
		}
	}
	if len(sizes) > 5 {
		sizes = sizes[:5]
	}
	parts := make([]string, 0, len(sizes))
	for _, size := range sizes {
		parts = append(parts, fmt.Sprintf("%s=%d", size.name, size.runes))
	}
	return strings.Join(parts, " ")
}

func logPlanGroundingInputScale(input domain.PlanGroundingInput, total, ceiling int) {
	slog.Info("[plan-grounding] 精确输入规模", planGroundingInputScaleAttrs(input, total, ceiling)...)
}

// PlanGroundingInputBudgetError is a local evidence-transport failure, not a
// grounding verdict or a request to rewrite the story to make evidence fit.
// Keep the original error chain for usage accounting and caller stop guards.
type PlanGroundingInputBudgetError struct{ Cause error }

func (e *PlanGroundingInputBudgetError) Error() string { return e.Cause.Error() }
func (e *PlanGroundingInputBudgetError) Unwrap() error { return e.Cause }

func planGroundingHasCodexExactTransport(model agentcore.ChatModel, input domain.PlanGroundingInput) bool {
	provider, ok := model.(agentcore.ProviderNamer)
	return input.Activation != nil && ok && provider.ProviderName() == "codex-cli"
}

func checkPlanGroundingInputBudget(model agentcore.ChatModel, input domain.PlanGroundingInput, raw []byte, derivedCeiling int) error {
	if planGroundingHasCodexExactTransport(model, input) {
		if len(raw) > planGroundingExactInputByteLimit {
			return &PlanGroundingInputBudgetError{Cause: fmt.Errorf("exact activation grounding input exceeds local byte limit: input_bytes=%d max_input_bytes=%d; cannot truncate authoritative evidence; no provider call", len(raw), planGroundingExactInputByteLimit)}
		}
		return nil
	}
	ceiling := effectivePlanGroundingRuneCeiling(derivedCeiling)
	if utf8.RuneCount(raw) > ceiling {
		return &PlanGroundingInputBudgetError{Cause: fmt.Errorf("exact plan grounding input exceeds %d runes (reviewer exact-packet ceiling derived from its resolved context window); cannot truncate authoritative evidence; %s", ceiling, planGroundingLargestPlanFields(input.Plan))}
	}
	return nil
}

func classifyPlanGroundingInputBudgetError(model agentcore.ChatModel, input domain.PlanGroundingInput, err error) error {
	if err == nil || !planGroundingHasCodexExactTransport(model, input) {
		return err
	}
	// Every executed Codex CLI failure has a typed usage receipt, even when
	// token counts are unavailable. Never reinterpret its message as a local
	// budget failure, even if a provider happens to use identical wording.
	var executed interface {
		error
		LLMUsage() (*agentcore.Usage, string)
	}
	if errors.As(err, &executed) {
		return err
	}
	var classified *PlanGroundingInputBudgetError
	if errors.As(err, &classified) {
		return err
	}
	// The committed adapter currently exposes these pre-dispatch errors as
	// plain Go errors. Recognize only its exact local-budget prefixes, not a
	// generic context/budget substring or arbitrary remote failure message.
	for _, prefix := range []string{
		"exact agent packet exceeds absolute input byte limit:",
		"exact agent packet exceeds configured operating budget:",
		"exact agent packet has invalid configured context budget; no provider call",
		"exact agent packet plus system/tool instructions exceeds Codex prompt budget ",
		"exact agent packet and latest complete feedback exceed Codex prompt budget ",
	} {
		if strings.HasPrefix(err.Error(), prefix) {
			return &PlanGroundingInputBudgetError{Cause: err}
		}
	}
	return err
}
