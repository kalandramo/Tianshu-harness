package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/contract"
)

// toolSchemaOracle 是 TS 侧真实注册表导出的工具 schema。
// 生成命令：npx tsx go/testdata/toolschema/gen-oracle.ts
type toolSchemaOracle struct {
	Description string `json:"description"`
	// InputSchema 是**有序**表达：{"__ordered": [[k, v], ...]}。
	InputSchema json.RawMessage `json:"inputSchema"`
	PropOrder   []string        `json:"propOrder"`
	Required    []string        `json:"required"`
}

func loadToolSchemaOracle(t *testing.T) map[string]toolSchemaOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "toolschema", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成：npx tsx go/testdata/toolschema/gen-oracle.ts", path, err)
	}
	var o map[string]toolSchemaOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// ordered 把 __ordered 结构还原成 Go 的有序表示（递归）。
func ordered(raw json.RawMessage) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return convertOrdered(v)
}

func convertOrdered(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if o, ok := x["__ordered"]; ok {
			arr, _ := o.([]any)
			om := wire.NewOrderedMap()
			for _, pair := range arr {
				p, _ := pair.([]any)
				if len(p) != 2 {
					continue
				}
				k, _ := p[0].(string)
				om.Set(k, convertOrdered(p[1]))
			}
			return om
		}
		// 非有序 map（不该出现，但防御）
		out := map[string]any{}
		for k, val := range x {
			out[k] = convertOrdered(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = convertOrdered(e)
		}
		return out
	default:
		return v
	}
}

// toolByName 从默认注册表取工具（供对账）。
func toolByName(t *testing.T, name string) Tool {
	t.Helper()
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	for _, tool := range reg.All() {
		if tool.Definition().Name == name {
			return tool
		}
	}
	t.Fatalf("注册表里找不到工具 %q", name)
	return nil
}

// TestToolSchemaParity —— 逐工具对账 input_schema 的**属性声明序**与**必填项**。
//
// 这是缓存命中率的防线：工具定义变化「打的是整个前缀（system+tools 段）」
// （src/api/openai-client.ts:630）——键序不同会让前缀缓存完全失效。
func TestToolSchemaParity(t *testing.T) {
	oracle := loadToolSchemaOracle(t)
	checked := 0

	for name, want := range oracle {
		checked++
		t.Run(name, func(t *testing.T) {
			tool := toolByName(t, name)
			def := tool.Definition()
			if def.InputSchema == nil {
				t.Fatal("Go 侧无 InputSchema")
			}

			// 1) 属性键集
			gotKeys := map[string]bool{}
			for k := range def.InputSchema.Properties {
				gotKeys[k] = true
			}
			for _, k := range want.PropOrder {
				if !gotKeys[k] {
					t.Errorf("缺少属性 %q（TS 有，Go 无）", k)
				}
			}
			wantSet := map[string]bool{}
			for _, k := range want.PropOrder {
				wantSet[k] = true
			}
			for k := range gotKeys {
				if !wantSet[k] {
					t.Errorf("多余属性 %q（Go 有，TS 无）", k)
				}
			}

			// 2) 声明序（PropOrder 必须与 TS 逐字一致）
			if len(def.InputSchema.PropOrder) != len(want.PropOrder) {
				t.Fatalf("PropOrder 长度不符：Go=%v TS=%v",
					def.InputSchema.PropOrder, want.PropOrder)
			}
			for i, k := range want.PropOrder {
				if def.InputSchema.PropOrder[i] != k {
					t.Errorf("PropOrder[%d]：Go=%q TS=%q\n  Go 完整=%v\n  TS 完整=%v",
						i, def.InputSchema.PropOrder[i], k,
						def.InputSchema.PropOrder, want.PropOrder)
				}
			}

			// 3) required
			if len(def.InputSchema.Required) != len(want.Required) {
				t.Errorf("required 长度不符：Go=%v TS=%v",
					def.InputSchema.Required, want.Required)
			} else {
				for i, r := range want.Required {
					if def.InputSchema.Required[i] != r {
						t.Errorf("required[%d]：Go=%q TS=%q", i, def.InputSchema.Required[i], r)
					}
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无工具")
	}
	t.Logf("对账了 %d 个工具", checked)
}

// knownSchemaDeviations 登记**有意的**与 TS schema 的偏离。
//
// # 为什么要白名单而不是改 oracle
//
// oracle 是 **TS 侧的忠实快照**（`gen-oracle.ts` 从 TS 真实注册表导出）。改它
// 会让「对账」失去意义——那等于把偏差藏进基准。故 oracle **保持 TS 原样**，
// 偏离在此**显式登记**，每条附**移除条件**。
//
// # 判据
//
// 只登记「Go 侧能力确实缺失、且诚实描述优于虚假承诺」的偏离。任何**能力对等**
// 却描述不同的条目，都是缺陷而非偏离——不该进这张表。
var knownSchemaDeviations = map[string]map[string]string{
	// tool → property → 偏离原因（含移除条件）
	"bash": {
		"run_in_background": "Go 侧无 sessionJobRegistry 设施（JobRegistry/JobStore 全库零命中），" +
			"参数被忽略、命令走前台。用户级验收已实测证实（第二十五刀）。" +
			"TS 描述承诺「转入后台并返回 job id」会误导模型——故改为诚实声明。" +
			"**移除条件**：job 子系统移植后，恢复 TS 原文案并从本表删除。",
	},
}

// deviationFor 返回该工具该属性的偏离原因；无偏离返回 ""。
func deviationFor(tool, prop string) string {
	if m, ok := knownSchemaDeviations[tool]; ok {
		return m[prop]
	}
	return ""
}

// deviatingProps 找出 Go 与 TS 之间**序列化后不同**的属性名。
//
// 判据：逐个属性单独序列化并比对（而非整串比对后猜位置）——这样能精确指出
// 是哪些属性偏离，白名单才可能「只放行已登记的」。
func deviatingProps(goSchema *contract.InputSchema, tsRaw json.RawMessage) []string {
	if len(tsRaw) == 0 {
		return nil
	}
	tsObj, _ := ordered(tsRaw).(*wire.OrderedMap)
	if tsObj == nil {
		return nil
	}
	tsPropsAny, _ := tsObj.Get("properties")
	tsProps, _ := tsPropsAny.(*wire.OrderedMap)
	if tsProps == nil {
		return nil
	}

	var out []string
	for _, p := range goSchema.PropOrder {
		goProp, ok := goSchema.Properties[p]
		if !ok {
			continue
		}
		tsProp, ok := tsProps.Get(p)
		if !ok {
			out = append(out, p)
			continue
		}
		goStr := serializeOrderedForTest(goProp)
		tsStr := serializeOrderedForTest(tsProp)
		if goStr != tsStr {
			out = append(out, p)
		}
	}
	return out
}

// TestToolSchemaByteParity —— 对账**完整序列化字节**（含嵌套键序与描述文本）。
//
// 比属性名对账更严格：捕捉「名字对但描述不同」「嵌套键序不同」这类差异。
func TestToolSchemaByteParity(t *testing.T) {
	oracle := loadToolSchemaOracle(t)
	checked := 0

	for name, want := range oracle {
		if len(want.InputSchema) == 0 {
			continue
		}
		checked++
		t.Run(name, func(t *testing.T) {
			tool := toolByName(t, name)
			def := tool.Definition()

			// Go 侧序列化：走与生产相同的 orderedProps 路径
			got := serializeSchemaForTest(def.InputSchema)
			wantStr := serializeOrderedForTest(ordered(want.InputSchema))

			// **判定差异属性**：找出 Go 与 TS 之间**描述不同**的属性名集合。
			// 只当这些属性**全部**已登记时才放行（部分登记 = 仍有未记录的偏离）。
			deviating := deviatingProps(def.InputSchema, want.InputSchema)
			if got != wantStr {
				// 差异定位：逐段找第一个不同处
				i := 0
				for i < len(got) && i < len(wantStr) && got[i] == wantStr[i] {
					i++
				}
				lo := i - 60
				if lo < 0 {
					lo = 0
				}
				gh, wh := i+60, i+60
				if gh > len(got) {
					gh = len(got)
				}
				if wh > len(wantStr) {
					wh = len(wantStr)
				}
				// **已知偏离白名单**：仅当**所有**差异属性都已登记时放行。
				// 报告而非静默——偏离必须是可见的（`t.Logf` 出现在 -v 输出里）。
				unregistered := []string{}
				for _, p := range deviating {
					if deviationFor(name, p) == "" {
						unregistered = append(unregistered, p)
					}
				}
				if len(deviating) > 0 && len(unregistered) == 0 {
					for _, p := range deviating {
						t.Logf("已知偏离（已登记）：%s.%s —— %s", name, p, deviationFor(name, p))
					}
					return
				}
				t.Errorf("序列化字节不符，首个差异在偏移 %d：\n  Go ...%s...\n  TS ...%s...\n  Go 全长=%d TS 全长=%d\n  差异属性=%v 未登记=%v",
					i, got[lo:gh], wantStr[lo:wh], len(got), len(wantStr), deviating, unregistered)
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无可用 schema")
	}
	t.Logf("逐字节对账了 %d 个工具", checked)
}

// serializeSchemaForTest 用生产的 orderedProps 路径序列化 schema。
func serializeSchemaForTest(s *contract.InputSchema) string {
	om := wire.NewOrderedMap().Set("type", s.Type)
	if s.Properties != nil {
		om.Set("properties", OrderedProps(s.Properties, s.PropOrder))
	}
	if len(s.Required) > 0 {
		req := make([]any, len(s.Required))
		for i, r := range s.Required {
			req[i] = r
		}
		om.Set("required", req)
	}
	return om.Marshal()
}

// serializeOrderedForTest 序列化 oracle 还原出的有序结构。
func serializeOrderedForTest(v any) string {
	if om, ok := v.(*wire.OrderedMap); ok {
		return om.Marshal()
	}
	return ""
}
