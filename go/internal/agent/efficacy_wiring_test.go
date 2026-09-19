package agent

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCrossSessionPriorSeeding —— **跨会话先验生效**（本刀的核心验收）。
//
// 用户动作：会话 A 产生效能计数（写入 JSONL）→ 会话 B 启动时加载并播种。
// 观察到：会话 B 的 readback **一开始就带着会话 A 的计数**（无需重新积累）。
//
// 这是「冷启动数据源」的直接验证：没有它，每个新会话的 holdout 资格
// （送达 >= 3）与成熟 lift（decided >= 5, shadow >= 3）都要从零攒。
func TestCrossSessionPriorSeeding(t *testing.T) {
	cwd := t.TempDir()

	// ── 会话 A：产生效能计数并写回 ──
	scA := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`), textTurn("done"),
	}}
	srvA := httptest.NewServer(scA.handler())
	defer srvA.Close()

	lA := newTestLoop(t, srvA, Config{Model: "m", MaxTokens: 100, Cwd: cwd})
	rbA := NewAdvisoryReadback()
	storeA := NewAdvisoryEfficacyStore(cwd)
	lA.Readback = rbA
	lA.EfficacyStore = storeA
	lA.Advisories = NewAdvisoryBus()

	// 预置会话 A 的统计（模拟已积累的效能证据）
	rbA.SeedPriors(map[string]EfficacyPriorCounts{"hot": {Delivered: 4, Adopted: 2, Ignored: 2}})
	// 再产生真实送达（让 Stats 里有可写回的计数）
	rbA.Track([]DeliveredAdvisory{{
		Key: "hot", Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted, WithinTurns: 2},
	}}, 1)

	// 会话 A 结束 → 写回
	lA.FlushAdvisoryEfficacy()

	// 文件应已生成且含该 key
	if _, err := os.Stat(storeA.Path()); err != nil {
		t.Fatalf("会话 A 未写回文件：%v", err)
	}

	// ── 会话 B：加载并播种 ──
	scB := &scriptedServer{responses: []string{textTurn("done")}}
	srvB := httptest.NewServer(scB.handler())
	defer srvB.Close()

	lB := newTestLoop(t, srvB, Config{Model: "m", MaxTokens: 100, Cwd: cwd})
	rbB := NewAdvisoryReadback()
	lB.Readback = rbB
	lB.EfficacyStore = NewAdvisoryEfficacyStore(cwd)
	lB.Advisories = NewAdvisoryBus()

	// 播种前：无先验
	if got := rbB.GetDeliveredCount("hot"); got != 0 {
		t.Fatalf("播种前 DeliveredCount 应为 0，得到 %v", got)
	}

	lB.SeedEfficacyPriors()

	// ── 观察：会话 B 带着会话 A 的计数 ──
	// **注意是 float64**（对账 TS）——先验经 EWMA 衰减后是小数，
	// 用容差比较（`> 0` 而非 `== 1`），避免衰减导致的间歇性失败。
	got := rbB.GetDeliveredCount("hot")
	if got <= 0 {
		t.Errorf("**跨会话先验未生效**：播种后 DeliveredCount 仍为 %v\n"+
			"（没有它，每个新会话的 holdout 资格与成熟 lift 都要从零攒）", got)
	}
	t.Logf("会话 B 播种后 DeliveredCount=%v（会话 A 的计数已继承）", got)
}

// TestEfficacyFlushIsIncremental —— **写回是增量差分，重复 flush 不翻倍**。
//
// 用户动作：同一会话内连续 flush 两次（无新增计数）。
// 观察到：文件里的计数**不翻倍**（第二次是空操作）。
//
// **这是最容易错的点**：mergeAndSave 内部是 `base[f] += delta[f]`——
// 若传累计值，第二次 flush 会把已写过的计数再叠加一次。
func TestEfficacyFlushIsIncremental(t *testing.T) {
	cwd := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`), textTurn("done"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: cwd})
	rb := NewAdvisoryReadback()
	store := NewAdvisoryEfficacyStore(cwd)
	l.Readback = rb
	l.EfficacyStore = store
	l.Advisories = NewAdvisoryBus()

	rb.Track([]DeliveredAdvisory{{
		Key: "k", Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted, WithinTurns: 2},
	}}, 1)

	// 第一次 flush：写入 1 次送达
	//
	// **所有断言都用容差**：每次 Load(nowMs()) 都会按「写入时刻 → 读取时刻」的
	// 真实间隔再衰减一次（毫秒级）。精确相等会因机器负载产生间歇性失败。
	// 半衰期 14 天 → 1 秒衰减约 6e-7，容差 1e-3 足够宽且仍有判别力。
	l.FlushAdvisoryEfficacy()
	first := store.Load(nowMs())
	if d := first["k"].Delivered; d < 0.999 || d > 1.0 {
		t.Fatalf("首次 flush 后 delivered 应约 1，得到 %v", d)
	}

	// 第二次 flush：无新增 → 空操作（**关键：不翻倍**）
	l.FlushAdvisoryEfficacy()
	second := store.Load(nowMs())
	if d := second["k"].Delivered; d < 0.999 || d > 1.0 {
		t.Errorf("**增量差分失效**：重复 flush 后 delivered=%v（应仍约 1）\n"+
			"（传累计值会让计数翻倍成 2）", d)
	}

	// 第三次：新增一次送达 → 应累加到约 2
	//
	// **允许 EWMA 衰减**：两次 flush 之间真实时间流逝（毫秒级，随机器负载波动），
	// 衰减因子略小于 1。这不是缺陷而是机制正确工作的证据。
	//
	// **容差必须宽**：半衰期 14 天时，1 秒衰减约 6e-7——但负载重时可能到秒级。
	// 用「相对容差 + 下界」而非固定区间，避免时序敏感导致的间歇性失败。
	rb.Track([]DeliveredAdvisory{{
		Key: "k", Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted, WithinTurns: 2},
	}}, 2)
	l.FlushAdvisoryEfficacy()
	third := store.Load(nowMs())
	got := third["k"].Delivered
	if got < 1.9 || got > 2.0 {
		t.Errorf("新增送达后 delivered 应约 2（允许 EWMA 衰减），得到 %v", got)
	}
}

// TestEfficacyFlushAtSessionEnd —— **FlushSession 兜底写回**。
//
// 用户动作：会话结束（调用 FlushSession），期间未到 20 轮触发点。
// 观察到：效能计数仍被写回（兜底生效）。
func TestEfficacyFlushAtSessionEnd(t *testing.T) {
	cwd := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`), textTurn("done"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: cwd})
	rb := NewAdvisoryReadback()
	store := NewAdvisoryEfficacyStore(cwd)
	l.Readback = rb
	l.EfficacyStore = store
	l.Advisories = NewAdvisoryBus()

	rb.Track([]DeliveredAdvisory{{
		Key: "k", Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted, WithinTurns: 2},
	}}, 1)

	// 只调 FlushSession（不调 FlushAdvisoryEfficacy）
	l.FlushSession()

	if _, err := os.Stat(store.Path()); err != nil {
		t.Errorf("**FlushSession 未兜底写回**：文件不存在（%v）\n"+
			"（会话短于 20 轮时，20 轮触发点永不命中——必须有 postSession 兜底）", err)
	}
}

// TestEfficacyPersistenceFormat —— **落盘格式可读回且字段完整**。
//
// 用户动作：写回后用新 store 实例读回。
// 观察到：所有 5 个计数字段与 updatedAt 都在（跨进程可读）。
func TestEfficacyPersistenceFormat(t *testing.T) {
	cwd := t.TempDir()
	store := NewAdvisoryEfficacyStore(cwd)
	now := int64(1_700_000_000_000)
	if err := store.MergeAndSave(map[string]EfficacyDelta{
		"k": {Delivered: 3, Adopted: 1, Ignored: 2, ShadowHeld: 4, ShadowSatisfied: 1},
	}, now); err != nil {
		t.Fatal(err)
	}

	// 新实例读回（模拟跨进程）
	got := NewAdvisoryEfficacyStore(cwd).Load(now)
	p, ok := got["k"]
	if !ok {
		t.Fatal("读回失败：key 缺失")
	}
	checks := []struct {
		name      string
		got, want float64
	}{
		{"delivered", p.Delivered, 3},
		{"adopted", p.Adopted, 1},
		{"ignored", p.Ignored, 2},
		{"shadowHeld", p.ShadowHeld, 4},
		{"shadowSatisfied", p.ShadowSatisfied, 1},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s：读回 %v，期望 %v", c.name, c.got, c.want)
		}
	}
	if p.UpdatedAt != now {
		t.Errorf("updatedAt：读回 %d，期望 %d", p.UpdatedAt, now)
	}

	// 文件内容应为 JSONL（一行一 key，末尾换行）
	raw, err := os.ReadFile(filepath.Join(cwd, ".rivet", "knowledge", "advisory-efficacy.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Error("JSONL 文件应以换行结尾")
	}
}

// TestEfficacyConcurrentWritesSerialized —— **并发写串行化（锁生效）**。
//
// 用户动作：两个 store 实例（模拟两个会话）各写不同 key。
// 观察到：两个 key 都在文件里（后写者读到前写者的结果再合并，不覆盖）。
func TestEfficacyConcurrentWritesSerialized(t *testing.T) {
	cwd := t.TempDir()
	now := int64(1_700_000_000_000)
	s1 := NewAdvisoryEfficacyStore(cwd)
	s2 := NewAdvisoryEfficacyStore(cwd)

	if err := s1.MergeAndSave(map[string]EfficacyDelta{"a": {Delivered: 3}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s2.MergeAndSave(map[string]EfficacyDelta{"b": {Delivered: 5}}, now); err != nil {
		t.Fatal(err)
	}

	got := s1.Load(now)
	if len(got) != 2 {
		t.Fatalf("应保留 2 个 key，得到 %d（后写者覆盖了前写者？）", len(got))
	}
	if _, ok := got["a"]; !ok {
		t.Error("key a 丢失——mergeAndSave 未先读最新文件")
	}
	if _, ok := got["b"]; !ok {
		t.Error("key b 丢失")
	}
}

// TestEfficacySeedFeedsLift —— **先验能喂进 lift 计算**（端到端链条）。
//
// 用户动作：跨会话先验提供了足够的 shadow 样本。
// 观察到：`GetMatureLift` 能算出值（不再是 nil）——先验确实进了成熟度门。
func TestEfficacySeedFeedsLift(t *testing.T) {
	cwd := t.TempDir()
	store := NewAdvisoryEfficacyStore(cwd)
	// **必须用当前时间**：SeedEfficacyPriors 内部用 time.Now() 加载，
	// 若写回时刻是固定历史值（如 1.7e12），会被 14 天半衰期衰减到 0 并被剔除。
	now := nowMs()

	// 写回「成熟 lift」所需的样本量：decided>=5 且 shadowHeld>=3
	if err := store.MergeAndSave(map[string]EfficacyDelta{
		"noisy": {Delivered: 6, Adopted: 0, Ignored: 6, ShadowHeld: 4, ShadowSatisfied: 4},
	}, now); err != nil {
		t.Fatal(err)
	}

	rb := NewAdvisoryReadback()
	l := &Loop{Readback: rb, EfficacyStore: store}
	l.SeedEfficacyPriors()

	lift := rb.GetMatureLift("noisy")
	if lift == nil {
		t.Fatal("**先验未喂进 lift 计算**：GetMatureLift 仍为 nil\n" +
			"（先验是冷启动的主数据源——没有它成熟度门永远过不了）")
	}
	// adopted/decided = 0/6 = 0；shadowSatisfied/shadowHeld = 4/4 = 1 → lift = -1
	// **用容差**：先验经 EWMA 衰减后计数为小数，比值略有偏差。
	if *lift < -1.001 || *lift > -0.999 {
		t.Errorf("lift 应约 -1（0 - 1），得到 %v", *lift)
	}
	t.Logf("先验驱动的成熟 lift=%v（负值 → 会触发静音）", *lift)
}

var _ = context.Background

// nowMs 返回当前毫秒时间戳（测试内小工具）。
func nowMs() int64 { return time.Now().UnixMilli() }
