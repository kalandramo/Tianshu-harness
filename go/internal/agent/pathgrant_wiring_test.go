package agent

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// outsidePlaceholder 标记「该参数应替换为工作区外的路径」。
//
// **为什么需要占位而非直接传路径**：越界路径必须相对 `run` 内部创建的
// `root`（`t.TempDir()`）来构造，而调用方在 `run` 之外。占位符让 `run`
// 在构造 root 后统一替换——避免测试碰真实系统文件（见 run 的注释）。
const outsidePlaceholder = "\x00OUTSIDE\x00"

// TestPathGrantGateWiring —— **越界路径授权门的端到端接线测试**。
//
// 判据（对账 TS `tool-pipeline.ts:1240-1249` 的 `pathGrantNeed` 分支）：
//
//   - **auto-safe 档** + 出界读写 → 拦下（指令性非重试拒绝）
//   - **skip 档** + 出界写 → 首触即授（放行，兑现「零审批打断」承诺）
//   - **区内**读写 → 两档都放行（不误拦）
//
// **为什么必须端到端**：单元测试只证明 `OutOfWorkspaceFilePaths` 判定正确；
// 若 `loop.go` 没消费它（悬空实现），单测照样全绿而模型仍撞墙。
// 本项目反复踩过「type-without-consumer」缺口（第五十刀的 `NeedsApproval`）。
func TestPathGrantGateWiring(t *testing.T) {
	// **为什么用「root 的兄弟目录」而非 /etc/passwd**（第八十一刀续订正）：
	// 原版用真实系统路径，而 **skip 档首触即授会对它授权**（对账 TS
	// `tool-pipeline.ts:1241-1249`——TS 同样无 forbiddenRoot 上界），
	// 于是 `write_file` 真的尝试写系统文件 → 挂住/污染。
	// 此前不暴露是因为授权看不到（`effectiveGrants` 修复前的 bug 掩盖了它）。
	// 出界判定只需「不在 root 之下」，用临时目录的兄弟即等价且安全。
	run := func(t *testing.T, mode, toolName string, args map[string]any) bool {
		t.Helper()
		root := t.TempDir()
		// 占位符 → root 的兄弟目录（确定「出界」，且不碰真实系统文件）。
		//
		// **必须在 toolTurnArgs 之前替换**：它把 args 序列化成 JSON 字符串，
		// 之后改 args 已无效（首版踩过——替换太晚，占位符原样进了请求）。
		outside := filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root))
		if err := os.MkdirAll(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(outside) })
		for k, v := range args {
			if sv, ok := v.(string); ok && sv == outsidePlaceholder {
				args[k] = filepath.Join(outside, "probe.txt")
			}
		}

		sc := &scriptedServer{responses: []string{
			toolTurnArgs("c1", toolName, args),
			textTurn("完成"),
		}}
		srv := httptest.NewServer(sc.handler())
		defer srv.Close()

		l := newTestLoop(t, srv, Config{
			Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: mode,
		})
		if err := l.Run(context.TODO(), "执行"); err != nil {
			t.Fatalf("Run 失败：%v", err)
		}
		for _, b := range sc.handlerBodies() {
			if strings.Contains(b, approvalBlockedMarker) {
				return true
			}
		}
		return false
	}

	t.Run("auto-safe_出界write_被拦", func(t *testing.T) {
		if !run(t, "auto-safe", "write_file", map[string]any{
			"file_path": outsidePlaceholder, "content": "x",
		}) {
			t.Error("auto-safe 档下出界写应被拦——无提示通道时降级为拒绝")
		}
	})

	t.Run("skip_出界write_放行", func(t *testing.T) {
		if run(t, "dangerously-skip-permissions", "write_file", map[string]any{
			"file_path": outsidePlaceholder, "content": "x",
		}) {
			t.Error("skip 档下出界写应首触即授（放行）——「零审批打断」承诺")
		}
	})

	t.Run("auto-safe_出界read_被拦", func(t *testing.T) {
		if !run(t, "auto-safe", "read_file", map[string]any{"file_path": outsidePlaceholder}) {
			t.Error("auto-safe 档下出界读应被拦")
		}
	})

	t.Run("区内读写_不误拦", func(t *testing.T) {
		for _, tc := range []struct {
			tool string
			args map[string]any
		}{
			{"write_file", map[string]any{"file_path": "a.txt", "content": "x"}},
			{"read_file", map[string]any{"file_path": "a.txt"}},
		} {
			if run(t, "auto-safe", tc.tool, tc.args) {
				t.Errorf("工作区内 %s 不该被拦（误拦会让工具完全不可用）", tc.tool)
			}
		}
	})

	t.Run("档位差异生效", func(t *testing.T) {
		// 同一出界写，两档结果必须**不同**——相同即说明档位未生效
		// （第五十刀前的实测形态：两档都 false）。
		args := map[string]any{"file_path": outsidePlaceholder, "content": "x"}
		autoSafe := run(t, "auto-safe", "write_file", args)
		skip := run(t, "dangerously-skip-permissions", "write_file", args)
		if autoSafe == skip {
			t.Errorf("档位差异未生效：auto-safe=%v skip=%v（应相反）", autoSafe, skip)
		}
	})
}
