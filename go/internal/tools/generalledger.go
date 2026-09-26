package tools

import (
	"context"
	"regexp"
	"strings"
	"time"

	ctxstore "github.com/kalandramo/tianshu/go/internal/context"
	"github.com/kalandramo/tianshu/go/internal/contract"
)

// generalledger.go —— 将星账本的两个工具（第七十七刀）。
//
// 对账 TS `src/tools/recall-general.ts` + `src/tools/record-general-finding.ts`。
// 存储层在 `internal/context/generalledger.go`。
//
// # 分工（TS 描述原文）
//
//	recall_capsule  取**方法论基因**（封存的原则）
//	recall_general  取**战绩记忆**（缺陷族/能力族 + 复发计数，持续生长）
//
// 出战验证/审查/勘探前召回账本；发现新战绩用 `record_general_finding` 追加。

// familySlugRe 校验 family 是 kebab-case slug。
//
// 对账 TS：`/^[a-z0-9][a-z0-9-]*$/`。
//
// **为什么强制 slug**：同族复发必须复用既有 slug（描述原文：「复用既有
// family slug 而非新造近义词」）——自由文本会让同一缺陷裂成多个族，
// recurrenceCount 失去意义。
var familySlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ── recall_general ──────────────────────────────────────────────────────────

type recallGeneralTool struct{ cwd string }

// RecallGeneral 创建 recall_general 工具。
func RecallGeneral(cwd string) Tool { return &recallGeneralTool{cwd: cwd} }

func (t *recallGeneralTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "recall_general",
		Description: "Pull a star general's battle ledger (跨会话战绩账本, " +
			"`.rivet/generals/<star>.md`) on demand. 分工：recall_capsule 取方法论基因" +
			"（封存的原则），recall_general 取战绩记忆（缺陷族/能力族 + 复发计数，持续生长）。" +
			"出战验证/审查/勘探前召回账本，带着上次的记忆作业；发现新战绩用 " +
			"`record_general_finding` 追加。",
		InputSchema: objSchema(map[string]any{
			"star": strProp("Star name (中文或 slug)，e.g. 瑶光 / yaoguang / 贪狼 / tanlang / 天梁."),
		}, "star"),
	}
}

func (t *recallGeneralTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	star, _ := p.Input["star"].(string)
	star = strings.TrimSpace(star)
	if star == "" {
		return contract.Result{Content: "recall_general: 需要提供 star。", IsError: true}, nil
	}

	cwd := t.cwd
	if p.Cwd != "" {
		cwd = p.Cwd
	}

	if _, ok := ctxstore.StarToGeneralSlug(star); !ok {
		return contract.Result{
			Content: "recall_general: 未知星域「" + star + "」。磁盘上的账本：" +
				joinOrNone(ctxstore.ListGenerals(cwd)) + "。",
			IsError: true,
		}, nil
	}

	slug, content, ok := ctxstore.ReadGeneralLedger(cwd, star)
	if !ok {
		return contract.Result{
			Content: "recall_general: 「" + star + "」尚无账本（`.rivet/generals/`）。磁盘上的账本：" +
				joinOrNone(ctxstore.ListGenerals(cwd)) +
				"。用 record_general_finding 记下第一条战绩即可创建。",
			IsError: true,
		}, nil
	}

	return contract.Result{
		Content:   content,
		UIContent: "召回将星账本：" + star + "（" + slug + ".md）",
	}, nil
}

func (t *recallGeneralTool) RequiresApproval(*CallParams) bool { return false }
func (t *recallGeneralTool) ConcurrencySafe() bool             { return true }
func (t *recallGeneralTool) Enabled() bool                     { return true }
func (t *recallGeneralTool) Timeout(*CallParams) time.Duration { return 0 }

// ── record_general_finding ──────────────────────────────────────────────────

type recordGeneralFindingTool struct{ cwd string }

// RecordGeneralFinding 创建 record_general_finding 工具。
func RecordGeneralFinding(cwd string) Tool { return &recordGeneralFindingTool{cwd: cwd} }

func (t *recordGeneralFindingTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "record_general_finding",
		Description: "Append a battle finding to a star general's ledger " +
			"(`.rivet/generals/<star>.md`) — 将星跨会话战绩累积的写入闭环。同族（family " +
			"slug 相同）复发则 recurrenceCount++ 并追加日期实例行，新族则新建条目。" +
			"瑶光记缺陷族、贪狼记能力族。写之前先 recall_general 查同族是否已存在，" +
			"复用既有 family slug 而非新造近义词。",
		InputSchema: objSchema(map[string]any{
			"star":   strProp("Star name (中文或 slug)，e.g. 瑶光 / yaoguang / 贪狼."),
			"family": strProp("Family slug (kebab-case)，e.g. always-true-on-missing-field. 同族复发必须复用既有 slug."),
			"note":   strProp("一行战绩描述（哪里发现/怎么处置/证据 commit）。"),
		}, "star", "family", "note"),
	}
}

func (t *recordGeneralFindingTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	star, _ := p.Input["star"].(string)
	family, _ := p.Input["family"].(string)
	note, _ := p.Input["note"].(string)
	star = strings.TrimSpace(star)
	family = strings.TrimSpace(family)
	note = strings.TrimSpace(note)

	if star == "" || family == "" || note == "" {
		return contract.Result{Content: "record_general_finding: star、family、note 均为必填。", IsError: true}, nil
	}
	if !familySlugRe.MatchString(family) {
		return contract.Result{
			Content: "record_general_finding: family 必须是 kebab-case slug（收到「" + family + "」）。",
			IsError: true,
		}, nil
	}

	cwd := t.cwd
	if p.Cwd != "" {
		cwd = p.Cwd
	}

	res, ok := ctxstore.AppendGeneralFinding(cwd, ctxstore.GeneralFindingInput{
		Star: star, Family: family, Note: note,
	})
	if !ok {
		return contract.Result{
			Content: "record_general_finding: 未知星域「" + star + "」。",
			IsError: true,
		}, nil
	}

	var content string
	if res.Created {
		content = "新族条目已建：" + family + "（" + res.Slug + ".md，recurrenceCount: 1）。"
	} else {
		content = "同族复发已记：" + family + "（" + res.Slug + ".md，recurrenceCount: " +
			itoa(res.RecurrenceCount) + "）。"
	}
	return contract.Result{
		Content:   content,
		UIContent: "将星战绩：" + res.Slug + "/" + family + " ×" + itoa(res.RecurrenceCount),
	}, nil
}

func (t *recordGeneralFindingTool) RequiresApproval(*CallParams) bool { return false }
func (t *recordGeneralFindingTool) ConcurrencySafe() bool             { return false }
func (t *recordGeneralFindingTool) Enabled() bool                     { return true }
func (t *recordGeneralFindingTool) Timeout(*CallParams) time.Duration { return 0 }

// joinOrNone 拼账本列表；空时返回「（无）」。
//
// 对账 TS：`listGenerals(cwd).join(', ') || '（无）'`。
func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "（无）"
	}
	return strings.Join(items, ", ")
}
