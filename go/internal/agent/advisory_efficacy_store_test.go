package agent

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// efficacyOracle 是 oracle.json 的结构。
type efficacyOracle struct {
	Constants struct {
		HalfLifeMs     int64   `json:"halfLifeMs"`
		PruneThreshold float64 `json:"pruneThreshold"`
		MaxKeys        int     `json:"maxKeys"`
	} `json:"constants"`
	Decay []struct {
		Name  string `json:"name"`
		Input struct {
			Key             string  `json:"key"`
			Delivered       float64 `json:"delivered"`
			Adopted         float64 `json:"adopted"`
			Ignored         float64 `json:"ignored"`
			ShadowHeld      float64 `json:"shadowHeld"`
			ShadowSatisfied float64 `json:"shadowSatisfied"`
			UpdatedAt       int64   `json:"updatedAt"`
		} `json:"input"`
		Now  int64 `json:"now"`
		Want struct {
			Delivered       float64 `json:"delivered"`
			Adopted         float64 `json:"adopted"`
			Ignored         float64 `json:"ignored"`
			ShadowHeld      float64 `json:"shadowHeld"`
			ShadowSatisfied float64 `json:"shadowSatisfied"`
			UpdatedAt       int64   `json:"updatedAt"`
		} `json:"want"`
	} `json:"decay"`
	Prune []struct {
		Name  string `json:"name"`
		Input struct {
			Key             string  `json:"key"`
			Delivered       float64 `json:"delivered"`
			Adopted         float64 `json:"adopted"`
			Ignored         float64 `json:"ignored"`
			ShadowHeld      float64 `json:"shadowHeld"`
			ShadowSatisfied float64 `json:"shadowSatisfied"`
			UpdatedAt       int64   `json:"updatedAt"`
		} `json:"input"`
		Now      int64 `json:"now"`
		WantKeep bool  `json:"wantKeep"`
	} `json:"prune"`
	Merge []struct {
		Name   string              `json:"name"`
		Rounds [][]json.RawMessage `json:"rounds"`
		Want   []struct {
			Key             string  `json:"key"`
			Delivered       float64 `json:"delivered"`
			Adopted         float64 `json:"adopted"`
			Ignored         float64 `json:"ignored"`
			ShadowHeld      float64 `json:"shadowHeld"`
			ShadowSatisfied float64 `json:"shadowSatisfied"`
		} `json:"want"`
	} `json:"merge"`
	MaxKeys struct {
		InputCount int    `json:"inputCount"`
		KeptCount  int    `json:"keptCount"`
		FirstKey   string `json:"firstKey"`
		LastKey    string `json:"lastKey"`
	} `json:"maxKeys"`
	Round3 []struct {
		In  float64 `json:"in"`
		Out float64 `json:"out"`
	} `json:"round3"`
}

func loadEfficacyOracle(t *testing.T) efficacyOracle {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "efficacy", "oracle.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 oracle 失败（先跑 node_modules/.bin/tsx go/testdata/efficacy/gen-oracle.ts）：%v", err)
	}
	var out efficacyOracle
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return out
}

func floatEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestEfficacyStoreConstants —— 常量与 TS 一致（半衰期 / 阈值 / 容量）。
func TestEfficacyStoreConstants(t *testing.T) {
	o := loadEfficacyOracle(t)
	if EfficacyHalfLifeMs != o.Constants.HalfLifeMs {
		t.Errorf("HalfLifeMs：Go=%d TS=%d", EfficacyHalfLifeMs, o.Constants.HalfLifeMs)
	}
	if efficacyPruneThreshold != o.Constants.PruneThreshold {
		t.Errorf("PruneThreshold：Go=%v TS=%v", efficacyPruneThreshold, o.Constants.PruneThreshold)
	}
	if efficacyMaxKeys != o.Constants.MaxKeys {
		t.Errorf("MaxKeys：Go=%d TS=%d", efficacyMaxKeys, o.Constants.MaxKeys)
	}
}

// TestEfficacyDecayParity —— **EWMA 衰减逐值对账**（含 14 天 / 28 天 / 未来时间戳）。
func TestEfficacyDecayParity(t *testing.T) {
	o := loadEfficacyOracle(t)
	for _, c := range o.Decay {
		t.Run(c.Name, func(t *testing.T) {
			got := decayedPrior(EfficacyPrior{
				Key: c.Input.Key, Delivered: c.Input.Delivered, Adopted: c.Input.Adopted,
				Ignored: c.Input.Ignored, ShadowHeld: c.Input.ShadowHeld,
				ShadowSatisfied: c.Input.ShadowSatisfied, UpdatedAt: c.Input.UpdatedAt,
			}, c.Now)
			checks := []struct {
				name      string
				got, want float64
			}{
				{"delivered", got.Delivered, c.Want.Delivered},
				{"adopted", got.Adopted, c.Want.Adopted},
				{"ignored", got.Ignored, c.Want.Ignored},
				{"shadowHeld", got.ShadowHeld, c.Want.ShadowHeld},
				{"shadowSatisfied", got.ShadowSatisfied, c.Want.ShadowSatisfied},
			}
			for _, ch := range checks {
				if !floatEq(ch.got, ch.want) {
					t.Errorf("%s：Go=%v TS=%v", ch.name, ch.got, ch.want)
				}
			}
			if got.UpdatedAt != c.Now {
				t.Errorf("updatedAt 应重置为 now：Go=%d want=%d", got.UpdatedAt, c.Now)
			}
		})
	}
}

// TestEfficacyPruneParity —— **剪枝阈值判定逐例对账**（含边界：恰等于阈值）。
func TestEfficacyPruneParity(t *testing.T) {
	o := loadEfficacyOracle(t)
	for _, c := range o.Prune {
		t.Run(c.Name, func(t *testing.T) {
			d := decayedPrior(EfficacyPrior{
				Key: c.Input.Key, Delivered: c.Input.Delivered, Adopted: c.Input.Adopted,
				Ignored: c.Input.Ignored, ShadowHeld: c.Input.ShadowHeld,
				ShadowSatisfied: c.Input.ShadowSatisfied, UpdatedAt: c.Input.UpdatedAt,
			}, c.Now)
			gotKeep := anyAtOrAbovePruneThreshold(d)
			if gotKeep != c.WantKeep {
				t.Errorf("保留判定：Go=%v TS=%v（decayed.delivered=%v）", gotKeep, c.WantKeep, d.Delivered)
			}
		})
	}
}

// TestEfficacyMergeAccumulates —— **增量合并叠加而非覆盖**（对账两个 merge 用例）。
func TestEfficacyMergeAccumulates(t *testing.T) {
	o := loadEfficacyOracle(t)
	for _, c := range o.Merge {
		t.Run(c.Name, func(t *testing.T) {
			dir := t.TempDir()
			st := NewAdvisoryEfficacyStore(dir)
			// BASE 时刻（与生成器一致）
			const base = 1_700_000_000_000
			for _, round := range c.Rounds {
				deltas := map[string]EfficacyDelta{}
				for _, raw := range round {
					// raw 是 [key, delta] 的 JSON 数组
					var pair []json.RawMessage
					if err := json.Unmarshal(raw, &pair); err != nil {
						t.Fatalf("解析 round 项失败：%v", err)
					}
					var key string
					if err := json.Unmarshal(pair[0], &key); err != nil {
						t.Fatalf("解析 key 失败：%v", err)
					}
					var d EfficacyDelta
					if err := json.Unmarshal(pair[1], &d); err != nil {
						t.Fatalf("解析 delta 失败：%v", err)
					}
					deltas[key] = d
				}
				// 每轮都衰减到同一 now——所以 round 之间没有时间流逝
				if err := st.MergeAndSave(deltas, base); err != nil {
					t.Fatalf("mergeAndSave 失败：%v", err)
				}
			}
			got := st.Load(base)
			if len(got) != len(c.Want) {
				t.Fatalf("key 数：Go=%d TS=%d", len(got), len(c.Want))
			}
			for _, w := range c.Want {
				g, ok := got[w.Key]
				if !ok {
					t.Fatalf("缺少 key %q", w.Key)
				}
				if !floatEq(g.Delivered, w.Delivered) || !floatEq(g.Adopted, w.Adopted) ||
					!floatEq(g.Ignored, w.Ignored) || !floatEq(g.ShadowHeld, w.ShadowHeld) ||
					!floatEq(g.ShadowSatisfied, w.ShadowSatisfied) {
					t.Errorf("key=%s：Go={d:%v a:%v i:%v sh:%v ss:%v} TS={d:%v a:%v i:%v sh:%v ss:%v}",
						w.Key, g.Delivered, g.Adopted, g.Ignored, g.ShadowHeld, g.ShadowSatisfied,
						w.Delivered, w.Adopted, w.Ignored, w.ShadowHeld, w.ShadowSatisfied)
				}
			}
		})
	}
}

// TestEfficacyMaxKeys —— **容量截断：保留 top-200，排序键 = delivered+shadowHeld 降序**。
func TestEfficacyMaxKeys(t *testing.T) {
	o := loadEfficacyOracle(t)
	dir := t.TempDir()
	st := NewAdvisoryEfficacyStore(dir)

	deltas := map[string]EfficacyDelta{}
	for i := 0; i < o.MaxKeys.InputCount; i++ {
		deltas["k"+itoaInt(i)] = EfficacyDelta{Delivered: float64(i)}
	}
	const base = 1_700_000_000_000
	if err := st.MergeAndSave(deltas, base); err != nil {
		t.Fatalf("mergeAndSave 失败：%v", err)
	}
	got := st.Load(base)
	if len(got) != o.MaxKeys.KeptCount {
		t.Errorf("保留 key 数：Go=%d TS=%d", len(got), o.MaxKeys.KeptCount)
	}
	// 首尾 key 校验（排序确定性）
	if _, ok := got[o.MaxKeys.FirstKey]; !ok {
		t.Errorf("应保留首 key %q（delivered 最大）", o.MaxKeys.FirstKey)
	}
	if _, ok := got[o.MaxKeys.LastKey]; !ok {
		t.Errorf("应保留末 key %q（第 200 名）", o.MaxKeys.LastKey)
	}
	// 被截掉的应是最小的几个
	if _, ok := got["k0"]; ok {
		t.Error("k0（delivered=0，最低）不该被保留")
	}
}

// TestEfficacyRound3 —— **序列化精度**（对账 round3）。
func TestEfficacyRound3(t *testing.T) {
	o := loadEfficacyOracle(t)
	for _, c := range o.Round3 {
		if got := efficacyRound3(c.In); !floatEq(got, c.Out) {
			t.Errorf("round3(%v)：Go=%v TS=%v", c.In, got, c.Out)
		}
	}
}

// TestEfficacyZeroDeltaNoWrite —— **零增量不落盘（文件都不创建）**。
func TestEfficacyZeroDeltaNoWrite(t *testing.T) {
	dir := t.TempDir()
	st := NewAdvisoryEfficacyStore(dir)
	if err := st.MergeAndSave(map[string]EfficacyDelta{"k": {}}, 1_700_000_000_000); err != nil {
		t.Fatalf("mergeAndSave 失败：%v", err)
	}
	if _, err := os.Stat(st.Path()); !os.IsNotExist(err) {
		t.Error("零增量不该创建文件")
	}
}

// TestEfficacyCorruptLinesSkipped —— **损坏行跳过不炸**。
func TestEfficacyCorruptLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	st := NewAdvisoryEfficacyStore(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".rivet", "knowledge"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "这不是 JSON\n" +
		`{"key":"good","delivered":3,"adopted":1,"ignored":2,"shadowHeld":3,"shadowSatisfied":1,"updatedAt":1700000000000}` + "\n" +
		`{"missing":"updatedAt"}` + "\n" +
		"\n"
	if err := os.WriteFile(st.Path(), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := st.Load(1_700_000_000_000)
	if len(got) != 1 {
		t.Fatalf("应只解析出 1 条有效记录，得到 %d 条", len(got))
	}
	if _, ok := got["good"]; !ok {
		t.Error("有效记录 good 应被解析")
	}
}

// TestEfficacyLoadDecaysAndPrunes —— **load 也衰减且过滤**（不只是 merge）。
func TestEfficacyLoadDecaysAndPrunes(t *testing.T) {
	dir := t.TempDir()
	st := NewAdvisoryEfficacyStore(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".rivet", "knowledge"), 0o755); err != nil {
		t.Fatal(err)
	}
	const base = 1_700_000_000_000
	const day = int64(24 * 60 * 60 * 1000)
	// fresh：keep；stale：56 天后衰减殆尽
	content := `{"key":"fresh","delivered":4,"adopted":2,"ignored":2,"shadowHeld":3,"shadowSatisfied":1,"updatedAt":` + itoaInt64(base) + `}` + "\n" +
		`{"key":"stale","delivered":0.2,"adopted":0.1,"ignored":0.1,"shadowHeld":0.2,"shadowSatisfied":0.1,"updatedAt":` + itoaInt64(base) + `}` + "\n"
	if err := os.WriteFile(st.Path(), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// 56 天后加载（4 个半衰期 → 1/16）
	got := st.Load(base + 56*day)
	if _, ok := got["stale"]; ok {
		t.Error("衰减殆尽的 key 应在 load 时被过滤")
	}
	fresh, ok := got["fresh"]
	if !ok {
		t.Fatal("fresh 应被保留")
	}
	// 4.0 / 16 = 0.25
	if !floatEq(fresh.Delivered, 0.25) {
		t.Errorf("fresh.delivered 应衰减为 0.25，得到 %v", fresh.Delivered)
	}
}

// itoaInt 是测试内的小工具（避免与包的 strconv 别名冲突）。
func itoaInt(n int) string { return itoaInt64(int64(n)) }

func itoaInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
