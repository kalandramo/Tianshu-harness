package agent

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// TestE2EGovernanceCooldownInPrompt —— **用户级验收**：
//
// 用户动作：真实 loop 跑多轮，每轮都投递同一个**注册冷却的 key**。
// 观察到：该提醒**不再每轮都出现在请求体里**——送达后 N 轮内被吞掉。
//
// **这修复了上轮发现的行为缺口**：上轮我观察到「同一提醒每轮重复出现在
// prompt」，当时判断抑制机制属于治理子系统（未移植）。本轮补上 key 冷却后，
// 注册 key 的重复被抑制。
func TestE2EGovernanceCooldownInPrompt(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "g.ts")
	if err := os.WriteFile(target, []byte("export const g = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 5 轮：每轮 postTurn 都投递同一个注册 key（readonly-spiral，3 轮冷却）
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"`+target+`"}`),
		toolTurn("c2", "read_file", `{"file_path":"`+target+`"}`),
		toolTurn("c3", "read_file", `{"file_path":"`+target+`"}`),
		toolTurn("c4", "read_file", `{"file_path":"`+target+`"}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	l := newTestLoopWithRegistry(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root}, reg)

	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	// 每轮 postTurn 都投递同一个注册 key
	p.Register(RuntimeHook{
		Name: "seed-cooldown-key", Phase: PhasePostTurn,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{
				Key: "readonly-spiral", Priority: 0.6,
				Category: CategoryDiscipline, Content: "该开始行动了",
			})
			return nil
		},
	})
	l.Hooks = p
	l.Advisories = bus

	if err := l.Run(t.Context(), "读几次文件"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	// 统计含该提醒的请求数
	var hits []int
	for i, body := range sc.bodies {
		if strings.Contains(body, "该开始行动了") {
			hits = append(hits, i)
		}
	}

	// **核心断言**：5 轮里该提醒不该每轮都出现
	if len(hits) >= len(sc.bodies) {
		t.Errorf("**注册 key 的提醒每轮都出现了**（%v，共 %d 个请求）——冷却未生效",
			hits, len(sc.bodies))
	}
	if len(hits) == 0 {
		t.Error("应至少送达一次（首轮）")
	}
	t.Logf("送达请求：%v（共 %d 个请求）", hits, len(sc.bodies))
}

// TestE2EGovernanceMutexInPrompt —— **互斥让位进入 prompt**。
//
// 用户动作：真实 loop 跑一轮，同轮投递 self-verify（债）与
// virtue-encouragement（表扬）。
// 观察到：请求体里只有 self-verify，表扬被让位。
func TestE2EGovernanceMutexInPrompt(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "m.ts")
	if err := os.WriteFile(target, []byte("export const m = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"`+target+`"}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	l := newTestLoopWithRegistry(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root}, reg)

	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "seed-mutex", Phase: PhasePostTurn,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{Key: "self-verify", Priority: 0.58, Category: CategoryDiscipline, Content: "有验证债未清"})
			bus.Submit(AdvisoryEntry{Key: "virtue-encouragement", Priority: 0.4, Category: CategoryEncouragement, Content: "干得漂亮"})
			return nil
		},
	})
	l.Hooks = p
	l.Advisories = bus

	if err := l.Run(t.Context(), "看看 m.ts"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	var sawDebt, sawPraise bool
	for _, body := range sc.bodies {
		if strings.Contains(body, "有验证债未清") {
			sawDebt = true
		}
		if strings.Contains(body, "干得漂亮") {
			sawPraise = true
		}
	}
	if !sawDebt {
		t.Error("验证债（winner）应进入 prompt")
	}
	if sawPraise {
		t.Error("**「有债仍表扬」是语义冲突**——表扬应让位，但进入了 prompt")
	}
}
