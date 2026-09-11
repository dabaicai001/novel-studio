package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chenhongyang/novel-studio/assets"
	"github.com/chenhongyang/novel-studio/internal/bootstrap"
	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/chenhongyang/novel-studio/internal/store"
	"github.com/chenhongyang/novel-studio/internal/tools"
	"github.com/voocel/agentcore"
)

const outlineAllOperationSystemBoundary = `你是 outline-all 的 Architect 主模型执行器。
宿主已经把本次冻结 operation 的完整任务与全部可见上下文直接交给你；不得转派、改派或请求其他代理。
你唯一拥有的工具是 save_foundation。严格执行任务中的 OUTLINE_ALL_INTENT，只完成一次回执授权的结构变更。
不得生成正文，不得调用动态检索，不得修改任务未授权的任何设定。save_foundation 明确返回 saved=true 且 outline_all=true 后立即结束。`

// agentcore treats MaxToolErrors=0 as unlimited rather than "no errors".
// Bound the complete regeneration attempts here; a positive MaxToolErrors
// would only disable the tool and leave the model looping until MaxTurns.
const outlineAllOperationMaxTurns = 4

// OutlineAllOperationProtocolDigest binds the direct Architect system
// boundary to outline-all's generation identity without exposing prompt text
// in receipts. Coordinator prompts are intentionally not part of this root.
func OutlineAllOperationProtocolDigest(architectLongPrompt string) (string, error) {
	return domain.DeterministicPlanningHash(struct {
		Version           string `json:"version"`
		ArchitectLong     string `json:"architect_long"`
		OperationBoundary string `json:"operation_boundary"`
	}{
		Version:           "outline-all-direct-architect.v1",
		ArchitectLong:     architectLongPrompt,
		OperationBoundary: outlineAllOperationSystemBoundary,
	})
}

type outlineAllOperationModel = directAgentModelIdentity

// RunOutlineAllOperation runs one frozen outline-all operation directly on
// the configured Architect primary model. It does not construct a
// Coordinator, does not expose delegation/research/prose capabilities, and
// never resolves configured fallback targets.
func RunOutlineAllOperation(
	ctx context.Context,
	cfg bootstrap.Config,
	bundle assets.Bundle,
	candidateOutputDir string,
	prompt string,
	recorders ...UsageRecorder,
) error {
	candidateOutputDir = strings.TrimSpace(candidateOutputDir)
	prompt = strings.TrimSpace(prompt)
	if candidateOutputDir == "" || prompt == "" {
		return fmt.Errorf("outline-all direct Architect requires candidate output dir and operation prompt")
	}
	absOutputDir, err := filepath.Abs(candidateOutputDir)
	if err != nil {
		return fmt.Errorf("outline-all direct Architect candidate path: %w", err)
	}
	cfg.OutputDir = absOutputDir
	cfg.DisableModelFailover = true
	cfg.DisableLiveRAG = true

	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return fmt.Errorf("outline-all direct Architect model set: %w", err)
	}
	model := models.ForRole("architect") // primary only: never ForRoleWithFailover
	provider, name, _ := models.CurrentSelection("architect")
	if model == nil || strings.TrimSpace(provider) == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("outline-all direct Architect primary model is unavailable")
	}

	st := store.NewStore(absOutputDir)
	if err := st.Init(); err != nil {
		return fmt.Errorf("outline-all direct Architect candidate init: %w", err)
	}
	return runOutlineAllOperationWithModel(ctx, cfg, bundle, st, prompt, outlineAllOperationModel{
		ChatModel: model,
		Provider:  provider,
		Name:      name,
	}, tools.NewSaveFoundationTool(st), recorders...)
}

func runOutlineAllOperationWithModel(
	ctx context.Context,
	cfg bootstrap.Config,
	bundle assets.Bundle,
	st *store.Store,
	prompt string,
	resolved outlineAllOperationModel,
	saveFoundation agentcore.Tool,
	recorders ...UsageRecorder,
) error {
	if st == nil || resolved.ChatModel == nil || saveFoundation == nil {
		return fmt.Errorf("outline-all direct Architect dependencies are incomplete")
	}
	if saveFoundation.Name() != "save_foundation" {
		return fmt.Errorf("outline-all direct Architect rejects capability %q", saveFoundation.Name())
	}
	if strings.TrimSpace(resolved.Provider) == "" || strings.TrimSpace(resolved.Name) == "" {
		return fmt.Errorf("outline-all direct Architect identity is incomplete")
	}

	systemPrompt := strings.TrimSpace(bundle.Prompts.ArchitectLong) + "\n\n" + outlineAllOperationSystemBoundary
	finalAuthorization, err := outlineAllFinalAuthorization(prompt)
	if err != nil {
		return err
	}
	arcContractAuthorization, err := outlineAllArcContractAuthorization(st, prompt)
	if err != nil {
		return err
	}
	if strings.TrimSpace(arcContractAuthorization) != "" {
		finalAuthorization = finalAuthorization + "\n\n" + arcContractAuthorization
	}
	logger := st.Sessions.SubAgentLogger(func(string) (string, string) {
		return resolved.Provider, resolved.Name
	})
	usage := outlineAllOperationUsage{resolved: resolved, record: func(msg agentcore.AgentMessage) {
		logger("architect_outline_all", prompt, msg)
		for _, record := range recorders {
			if record != nil {
				record("architect_outline_all", msg)
			}
		}
	}}

	var mutationMu sync.Mutex
	mutationComplete := false
	pendingRetryReminder := ""
	stopAfterSuccessfulSave := func(toolName string, result json.RawMessage) bool {
		if toolName != "save_foundation" || !successfulOutlineAllSave(result) {
			return false
		}
		mutationMu.Lock()
		mutationComplete = true
		mutationMu.Unlock()
		return true
	}
	preventSecondMutation := func(
		ctx context.Context,
		call agentcore.ToolCall,
		next agentcore.ToolExecuteFunc,
	) (json.RawMessage, error) {
		mutationMu.Lock()
		alreadyComplete := mutationComplete
		mutationMu.Unlock()
		if alreadyComplete {
			return nil, fmt.Errorf("outline-all direct Architect already completed its one authorized save_foundation mutation")
		}
		result, err := next(ctx, call.Args)
		mutationMu.Lock()
		if err != nil {
			pendingRetryReminder = outlineAllRetryReminder(finalAuthorization, err)
		} else if successfulOutlineAllSave(result) {
			mutationComplete = true
			pendingRetryReminder = ""
		}
		mutationMu.Unlock()
		return result, err
	}
	takeRetryReminder := func() []agentcore.AgentMessage {
		mutationMu.Lock()
		defer mutationMu.Unlock()
		if pendingRetryReminder == "" {
			return nil
		}
		reminder := pendingRetryReminder
		pendingRetryReminder = ""
		return []agentcore.AgentMessage{agentcore.UserMsg(reminder)}
	}

	model := &outlineAllUsageModel{ChatModel: resolved.ChatModel, usage: &usage}
	attachDirectUsageLifecycle(ctx, model, "architect_outline_all")
	events := agentcore.AgentLoop(
		ctx,
		[]agentcore.AgentMessage{
			// Keep the persisted operation prompt byte-for-byte intact. The final
			// message is derived only from its host-issued intent marker, so a
			// large historical context cannot become the model's effective target.
			agentcore.UserMsg(prompt),
			agentcore.UserMsg(finalAuthorization),
		},
		agentcore.AgentContext{
			SystemPrompt: systemPrompt,
			Tools:        []agentcore.Tool{saveFoundation},
		},
		agentcore.LoopConfig{
			Model:               model,
			MaxTurns:            cappedMaxTurns(cfg.ResolveMaxTurns("architect", outlineAllOperationMaxTurns), outlineAllOperationMaxTurns),
			MaxRetries:          subagentMaxRetries,
			MaxToolErrors:       0,
			ThinkingLevel:       resolvedRoleThinking(resolved.ChatModel, cfg, "architect"),
			ToolsAreIdempotent:  false,
			CacheLastMessage:    promptCacheControl,
			PromptCacheKey:      agentPromptCacheKey("architect_outline_all", st.Dir(), prompt),
			GetSteeringMessages: takeRetryReminder,
			Middlewares:         []agentcore.ToolMiddleware{preventSecondMutation},
			StopAfterToolResult: stopAfterSuccessfulSave,
			OnMessage: func(msg agentcore.AgentMessage) {
				usage.message(msg)
			},
		},
	)
	var runErr error
	for event := range events {
		if event.Type == agentcore.EventError || event.Type == agentcore.EventRetry {
			usage.observeError(event.Err)
		}
		if event.Type == agentcore.EventError && event.Err != nil {
			runErr = event.Err
		}
	}
	if runErr != nil {
		return fmt.Errorf("outline-all direct Architect run: %w", runErr)
	}
	mutationMu.Lock()
	completed := mutationComplete
	mutationMu.Unlock()
	if !completed {
		return fmt.Errorf("outline-all direct Architect returned without a successful outline-all save_foundation mutation")
	}
	return nil
}

func outlineAllRetryReminder(finalAuthorization string, toolErr error) string {
	return fmt.Sprintf(
		"%s\n\n[LAST SAVE_FOUNDATION REJECTION / MUST FIX]\n"+
			"上一轮 save_foundation 未完成授权变更。宿主返回的精确错误如下：\n%s\n"+
			"保持上述 operation、type、volume、arc 和 content 数量不变，只修正该错误后再调用一次 save_foundation。",
		strings.TrimSpace(finalAuthorization),
		toolErr.Error(),
	)
}

func outlineAllFinalAuthorization(prompt string) (string, error) {
	action, err := domain.ParseOutlineAllIntent(prompt)
	if err != nil {
		return "", fmt.Errorf("outline-all direct Architect final authorization: %w", err)
	}

	var summary, target string
	switch action.Type {
	case domain.OutlineAllActionPlanStructure:
		summary = fmt.Sprintf("operation=%d type=%s", action.Operation, action.Type)
		target = "下一步且唯一写操作：save_foundation(type=\"plan_structure\", content=<完整全书 VolumeOutline 骨架数组>)。每卷含 index/title/theme 与有序 arcs；每弧含 index/title/goal 与 estimated_chapters（>=1，无上限，由你按剧情自定），chapters 必须为空。卷数与全书总章数必须落在 estimated_scale 声明的范围内。不得提供 volume/arc 参数，不得展开任何章节。"
	case domain.OutlineAllActionAppendVolume:
		summary = fmt.Sprintf(
			"operation=%d type=%s volume=%d expected_chapter_span=%d expected_arc_spans=%s final_skeleton=%t",
			action.Operation,
			action.Type,
			action.Volume,
			action.ExpectedChapterSpan,
			action.ExpectedArcSpans,
			action.FinalSkeleton,
		)
		target = fmt.Sprintf(
			"下一步且唯一写操作：save_foundation(type=%q, volume=%d, content=<恰好预留%d章、弧跨度严格为[%s]的VolumeOutline>)。不得提供arc。",
			action.Type,
			action.Volume,
			action.ExpectedChapterSpan,
			action.ExpectedArcSpans,
		)
	case domain.OutlineAllActionMapContracts:
		summary = fmt.Sprintf(
			"operation=%d type=%s expected_chapter_span=%d",
			action.Operation,
			action.Type,
			action.ExpectedChapterSpan,
		)
		target = fmt.Sprintf(
			"下一步且唯一写操作：save_foundation(type=%q, content=<覆盖冻结全书%d章的ArcContractAssignment数组>)。不得提供volume或arc。",
			action.Type,
			action.ExpectedChapterSpan,
		)
	case domain.OutlineAllActionExpandArc, domain.OutlineAllActionReviseArc:
		summary = fmt.Sprintf(
			"operation=%d type=%s volume=%d arc=%d expected_chapter_span=%d",
			action.Operation,
			action.Type,
			action.Volume,
			action.Arc,
			action.ExpectedChapterSpan,
		)
		target = fmt.Sprintf(
			"下一步且唯一写操作：save_foundation(type=%q, volume=%d, arc=%d, content=<恰好%d个OutlineEntry>)。",
			action.Type,
			action.Volume,
			action.Arc,
			action.ExpectedChapterSpan,
		)
	default:
		// ParseOutlineAllIntent validates the action type. Keep this defensive
		// branch fail-closed if validation and formatting ever drift apart.
		return "", fmt.Errorf("outline-all direct Architect final authorization rejects action type %q", action.Type)
	}

	return fmt.Sprintf(
		"[FINAL AUTHORIZED ACTION / HOST ENFORCED]\n"+
			"前文 MODEL_VISIBLE_CONTEXT 中出现的其他卷、弧和历史 operation 全部只读，绝不是可选目标。\n"+
			"%s\n"+
			"%s\n"+
			"任何其他 type、volume、arc 或 content 数量都无效；不得修改前文中的其他弧。工具返回 saved=true 后立即结束。",
		summary,
		target,
	), nil
}

// outlineAllArcContractAuthorization renders the host-frozen contract_refs
// whitelist for the arc this operation may write. The model is otherwise
// asked to infer "this arc owns no contracts" from a 70k+ token context that
// lists every other arc's refs, and any globally valid ref attached to the
// wrong arc is rejected as unknown_contract_ref no matter how concretely the
// chapter realizes it. Naming the authorized set removes that inference.
func outlineAllArcContractAuthorization(st *store.Store, prompt string) (string, error) {
	if st == nil {
		return "", nil
	}
	action, err := domain.ParseOutlineAllIntent(prompt)
	if err != nil {
		return "", fmt.Errorf("outline-all arc contract authorization: %w", err)
	}
	if action.Type != domain.OutlineAllActionExpandArc && action.Type != domain.OutlineAllActionReviseArc {
		return "", nil
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		// Fail soft: the clause is a clarification, not a gate. Structural
		// authorization stays with the frozen intent and save_foundation.
		return "", nil
	}
	cursor := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			start := cursor
			cursor += arc.ChapterSpan()
			if volume.Index != action.Volume || arc.Index != action.Arc {
				continue
			}
			last := start + max(0, arc.ChapterSpan()) - 1
			lines := []string{
				"[HOST FROZEN CONTRACT REFS / ARC LOCAL]",
				fmt.Sprintf("本弧 V%dA%d 覆盖全局第 %d-%d 章。", volume.Index, arc.Index, start, last),
			}
			if len(arc.ContractRefs) == 0 {
				lines = append(lines,
					"本弧授权的 contract_refs 为空：content 中每一个 OutlineEntry 的 contract_refs 必须是 []，不得出现任何 ref。",
					"某个合同的语义与本弧剧情相关也不构成授权：它已被 map_contracts 分配给别的弧，挂到本弧一律以 unknown_contract_ref 拒绝，且补写内容无法修复。",
				)
				return strings.Join(lines, "\n"), nil
			}
			items := make([]string, 0, len(arc.ContractRefs))
			for _, ref := range arc.ContractRefs {
				items = append(items, fmt.Sprintf(
					"- id=%q：必须挂在全局第 %d 章，且该章 core_event/scenes 要具体落实下面这段 planned_resolution（行动者+行动+终态）：%s",
					ref.ID, ref.PlannedPayoffChapter, ref.PlannedResolution,
				))
			}
			lines = append(lines,
				"本弧授权的 contract_refs 只有下列项，每一项必须出现在指定章号且各恰好出现一次。**写 {\"id\": \"...\"} 即可**：kind / source_digest / planned_payoff_chapter / planned_resolution 由宿主按 id 自动补全，不需要你复制，也不要改写它们：",
				strings.Join(items, "\n"),
				"未列出的 id 一律非法（unknown_contract_ref）；列出的 id 未出现在其指定章号即为 payoff_count 不足。",
			)
			return strings.Join(lines, "\n"), nil
		}
	}
	return "", nil
}

func successfulOutlineAllSave(result json.RawMessage) bool {
	var decoded struct {
		Saved      bool   `json:"saved"`
		OutlineAll bool   `json:"outline_all"`
		Type       string `json:"type"`
	}
	if json.Unmarshal(result, &decoded) != nil || !decoded.Saved || !decoded.OutlineAll {
		return false
	}
	switch decoded.Type {
	case "plan_structure", "append_volume", "map_contracts", "expand_arc", "revise_arc":
		return true
	default:
		return false
	}
}
