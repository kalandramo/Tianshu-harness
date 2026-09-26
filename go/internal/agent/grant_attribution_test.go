package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// grant_attribution_test.go —— 闭合「skip 档 E2E 假绿」（第八十一刀审查发现）。
//
// # 问题（对抗式审查 HIGH）
//
// `requestpathaccess_wiring_test.go` 与 `acceptance_requestpathaccess_test.go`
// 都在 **skip 档**上验证「授权后写文件落盘」。但 skip 档的门链本身会对
// `write_file` **首触即授**（`loop.go` 的 pathGrant 门）——所以**即使
// `request_path_access` 是空操作**，那两步也会通过。
//
// 实测确证：skip 档下**完全不调** `request_path_access`，写工作区外文件仍落盘。
// → 那两个测试**无法把工具的贡献与自动授权区分开**（假绿）。
//
// # 本文件的解法：归因测试
//
// 不靠「落盘与否」（两因共存），而靠**归因**：
//  1. `GrantPath` 回调计数——区分授权来自「工具显式调用」还是「门链自动授予」
//  2. manual 档 + 预置授权——该档无自动授予，工具的贡献**唯一可归因**
func TestGrantAttributionToolVsAutoGrant(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	out := filepath.Join(base, "out")
	for _, d := range []string{ws, out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// ── 场景 A：skip 档，**不调工具** → 门链自动授予（工具贡献为零）──
	regA := tools.NewDefaultRegistry(tools.Options{Cwd: ws})
	lA := New(Config{Cwd: ws, SessionID: "attr-a", ApprovalMode: "dangerously-skip-permissions"}, nil, regA)
	// 用 write_file 触发门链自动授予。
	_ = lA.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": filepath.Join(out, "auto.txt"), "content": "x"},
	})
	// 门链自动授予**不经** GrantPath 回调（它直接调 l.pathGrants.GrantPath）。
	// 故此处断言的是「自动授予确实发生」——这正是假绿的来源。
	if !lA.pathGrants.IsWriteGranted(filepath.Join(out, "auto.txt"), lA.cfg.Cwd) {
		t.Fatal("前提不成立：skip 档门链应首触即授")
	}
	t.Log("场景 A：skip 档门链自动授予已发生（**未经** request_path_access）")

	// ── 场景 B：auto-safe 档（**无路径自动授予**）+ 显式授权 → 工具贡献唯一可归因 ──
	//
	// **为什么用 auto-safe 而非 manual**：manual 档下**所有写工具**都需逐次人工
	// 批准（`decideApprovalGate` 的 manual 分支），审批门**先于** pathGrant 门——
	// 故授权后写操作仍被审批门拦，无法验证「授权生效」。
	// auto-safe 档只拦高风险（写工具 risk 为 low → 放行），且**不做路径自动授予**，
	// 是唯一能干净归因的档位。
	regB := tools.NewDefaultRegistry(tools.Options{Cwd: ws})
	lB := New(Config{Cwd: ws, SessionID: "attr-b", ApprovalMode: "auto-safe"}, nil, regB)

	// B1：授权前，写工作区外文件**应被拦**（manual 档无自动授予）。
	before := lB.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": filepath.Join(out, "manual.txt"), "content": "y"},
	})
	if !before.IsError {
		t.Fatalf("B1 前提不成立：auto-safe 档无路径授权应被拦，实得 %q", before.Content)
	}
	if _, err := os.Stat(filepath.Join(out, "manual.txt")); err == nil {
		t.Fatal("B1：被拦的调用不得落盘")
	}
	t.Logf("场景 B1：auto-safe 档无路径授权 → 被拦（%s）", truncateStr(before.Content, 50))

	// B2：授权。**为什么经回调而非 lB.executeTool 调工具**：
	// `request_path_access` 的 `RequiresUnconditionalApproval` 恒 true，在
	// auto-safe 档被审批门硬拒（Go 侧无弹窗通道——对账 TS 的弹窗批准后授权）。
	// 本测试目标是「授权产出是否被工具内部看到」，故经回调直授。
	grantFn := lB.grantPathFunc()
	if grantFn == nil {
		t.Fatal("B2：grantPathFunc 不应为 nil（pathGrants 已初始化）")
	}
	grantFn(out, tools.GrantWrite, lB.cfg.Cwd)

	// B3：授权后，同一操作**应放行**——此处的放行**唯一归因于该授权**
	//（manual 档无自动授予，B1 已证未授权时被拦）。
	after := lB.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": filepath.Join(out, "manual.txt"), "content": "y"},
	})
	if after.IsError {
		t.Fatalf("B3：授权后应放行，实得 %q", after.Content)
	}
	got, err := os.ReadFile(filepath.Join(out, "manual.txt"))
	if err != nil {
		t.Fatalf("B3：应落盘：%v", err)
	}
	if string(got) != "y" {
		t.Fatalf("B3：内容应为 y，实得 %q", got)
	}
	t.Log("场景 B3：auto-safe 档授权后 → 落盘（**唯一归因于该授权**）")
}

// TestGrantAttributionGlobGrepSeeGrants —— glob/grep/file_info/diff 也消费会话授权。
//
// 对账审查的 MEDIUM 发现：这 4 个工具此前硬传 nil grants，导致「已授权的工作区外
// 目录对它们仍被拒」——与 `request_path_access` 的成功文案「文件工具与 bash 现在
// 可以在此读写路径」不符（过度承诺）。本测试锁定修复后的行为。
func TestGrantAttributionGlobGrepSeeGrants(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	out := filepath.Join(base, "out")
	for _, d := range []string{ws, out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(out, "target.txt"), []byte("outside-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: ws})
	l := New(Config{Cwd: ws, SessionID: "attr-glob", ApprovalMode: "auto-safe"}, nil, reg)

	// ① 授权前：grep 工作区外目录应被拦。
	before := l.executeTool(context.Background(), toolCall{
		name:  "grep",
		input: map[string]any{"pattern": "outside-marker", "path": out},
	})
	if !before.IsError {
		t.Fatalf("① 未授权时 grep 工作区外应被拦，实得 %q", truncateStr(before.Content, 80))
	}
	t.Logf("① 未授权：grep 被拦（%s）", truncateStr(before.Content, 50))

	// ② 授权该目录。
	l.grantPathFunc()(out, tools.GrantWrite, l.cfg.Cwd)

	// ③ 授权后：grep 应放行并找到内容。
	after := l.executeTool(context.Background(), toolCall{
		name:  "grep",
		input: map[string]any{"pattern": "outside-marker", "path": out},
	})
	if after.IsError {
		t.Fatalf("③ 授权后 grep 应放行，实得 %q", truncateStr(after.Content, 120))
	}
	if !strings.Contains(after.Content, "outside-marker") {
		t.Fatalf("③ 应找到内容，实得 %q", truncateStr(after.Content, 120))
	}
	t.Log("③ 授权后：grep 放行并命中（消费了会话授权）")
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
