package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// hook_timeout_test.go —— 超时路径的两个缺陷（第七十一刀）。
//
// # 缺陷 1（产品）：超时时 `Output` 为空——错误信息被吞
//
// `runHook` 最后一行是 `Output: strings.TrimSpace(out)`——超时时 `out` 是
// **空串**（脚本还没输出就被杀），而 `ErrTimeout` **没有写进 `Output`**。
// 后果：用户/模型看到「hook 失败」但**不知道为什么**——无法区分
// 「脚本不存在」「超时」「退出码非零」。
//
// # 缺陷 2（测试）：`DefaultTimeoutMs = 5000` 在并发负载下不够
//
// `go test ./...` 并行多包时 CPU/IO 争抢 → 脚本（起 `sh` 进程）启动慢 →
// 撞 5 秒超时 → `TestRunForEventExecutesScript` 间歇失败（实测复现率约 1/3）。
//
// **根因证据（探针）**：把 `timeoutMs` 降到 1ms，产出 `Ok=false Output=""`
// ——与全量失败的形态**逐字一致**。
//
// # 为什么这**不是** trust 污染（我上一轮的归因错了）
//
// 曾推测是 `RIVET_TRUST_PROJECT` 环境变量被并发测试覆盖。核实后否定：
//   - `t.Setenv` 在包内是串行安全的（本包无 `t.Parallel`）
//   - 全量跑是**每包独立进程**，跨包不会共享环境变量
//   - 失败耗时 **5.01s** = `DefaultTimeoutMs`，是超时的指纹
//   - 单包连跑 18 次全绿（负载低时不触发）

// TestHookTimeoutReportsReason —— 缺陷 1 的 RED：超时必须给出可诊断的原因。
//
// 判据：`Output` **非空**且能识别为超时——否则调用方无法区分失败类型。
func TestHookTimeoutReportsReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 shebang")
	}
	dir := t.TempDir()
	t.Setenv("RIVET_TRUST_PROJECT", "1")
	script := filepath.Join(dir, "slow.sh")
	// 睡眠远超超时——确保撞超时路径。
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"slow.sh","timeoutMs":50}]}`)

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	res := r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})

	if len(res) != 1 {
		t.Fatalf("应有 1 条结果，实得 %d", len(res))
	}
	if res[0].Ok {
		t.Fatal("超时不该判为 Ok")
	}
	// **核心断言**：失败必须带可诊断原因。
	if strings.TrimSpace(res[0].Output) == "" {
		t.Error("**超时的 Output 为空**——调用方无法区分「超时」「脚本不存在」" +
			"「退出码非零」。ErrTimeout 必须写进 Output。")
	}
	// **修正记录**：初版断言查子串 `"timeout"`，但 `ErrTimeout` 的文本是
	// `"hook script timed out"`——**不含** `"timeout"`（是 `"timed out"`）。
	// 断言写错而非实现错。改用两个实际可能出现的形态。
	low := strings.ToLower(res[0].Output)
	if !strings.Contains(low, "timed out") && !strings.Contains(low, "timeout") &&
		!strings.Contains(res[0].Output, "超时") {
		t.Errorf("失败原因应指明是超时，实得: %q", res[0].Output)
	}
}

// TestHookNonZeroExitReportsReason —— **反面对照**：非零退出码的原因也要可见。
//
// 超时只是「失败要有原因」的一个实例——退出码非零同样。
func TestHookNonZeroExitReportsReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 shebang")
	}
	dir := t.TempDir()
	t.Setenv("RIVET_TRUST_PROJECT", "1")
	script := filepath.Join(dir, "fail.sh")
	body := "#!/bin/sh\necho boom >&2\nexit 3\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"fail.sh"}]}`)

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	res := r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})

	if len(res) != 1 {
		t.Fatalf("应有 1 条结果，实得 %d", len(res))
	}
	if res[0].Ok {
		t.Fatal("退出码 3 不该判为 Ok")
	}
	// 脚本的 stderr 应可见（当前实现把 stderr 也收进 buf）。
	if !strings.Contains(res[0].Output, "boom") {
		t.Errorf("失败脚本的 stderr 应可见，实得: %q", res[0].Output)
	}
}

// TestHookTimeoutDoesNotBlockForever —— 超时必须**真的中断**（不是只标记失败）。
//
// 不变量：`RunForEvent` 在超时后应快速返回，不能等脚本跑完（5s）。
// 这钉住「超时生效」本身——防止修「报原因」时把超时逻辑改坏。
func TestHookTimeoutDoesNotBlockForever(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 shebang")
	}
	dir := t.TempDir()
	t.Setenv("RIVET_TRUST_PROJECT", "1")
	script := filepath.Join(dir, "slow2.sh")
	// 睡 10s——若超时不生效，测试会明显拖长。
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 10\n"), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"slow2.sh","timeoutMs":100}]}`)

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	start := time.Now()
	res := r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})
	elapsed := time.Since(start)

	if len(res) != 1 {
		t.Fatalf("应有 1 条结果，实得 %d", len(res))
	}
	// 宽限：超时 100ms + 进程树回收开销，2s 足够。
	if elapsed > 2*time.Second {
		t.Errorf("超时未生效：耗时 %v（超时设 100ms，脚本睡 10s）", elapsed)
	}
}
