package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSkillFile 在 dir 下写一个扁平 skill 文件（`<name>.md`）。
func writeSkillFile(t *testing.T, dir, name, desc string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\nBody of " + name + ".\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeDirSkill 在 dir 下写一个目录型 skill（`<name>/SKILL.md`）。
func writeDirSkill(t *testing.T, dir, name, desc string) {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\nBody of " + name + ".\n"
	if err := os.WriteFile(filepath.Join(sub, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAssembleLayerPrecedence —— **本刀的核心不变量**。
//
// 五层优先级靠**加载顺序**实现（`Register` 同名覆盖，后加载者胜）。
// 每层放一个**同名** skill，断言最终生效的是优先级最高的那层。
//
// 这是最容易静默出错的地方：顺序写反了功能看起来照常工作（skill 都能加载），
// 只是覆盖方向反了——只有逐层断言才能发现。
func TestAssembleLayerPrecedence(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()

	// 同名 skill 放在**全部四层**（builtin 层用真实内置，不冲突）。
	name := "precedence-probe"
	writeSkillFile(t, filepath.Join(home, ".agents", "skills"), name, "from-global-agents")
	writeSkillFile(t, filepath.Join(home, ".rivet", "skills"), name, "from-global-rivet")
	writeSkillFile(t, filepath.Join(cwd, ".agents", "skills"), name, "from-project-agents")
	writeSkillFile(t, filepath.Join(cwd, ".rivet", "skills"), name, "from-project-rivet")

	reg, _ := AssembleWithHome(cwd, home)

	got, ok := reg.Get(name)
	if !ok {
		t.Fatal("skill 未加载")
	}
	if got.Description != "from-project-rivet" {
		t.Errorf("生效的是 %q，want %q（项目 .rivet/skills 应最高优先级）",
			got.Description, "from-project-rivet")
	}
	if got.Source != SourceRivet {
		t.Errorf("source = %q, want %q", got.Source, SourceRivet)
	}
}

// 逐层验证：按**加载顺序**（低 → 高）逐层累积，每加一层生效的应变成新加的。
//
// **为什么需要**：只测「最高层胜出」不能排除「顺序完全反了但恰好最高层
// 也在最后」的巧合——逐层累积才能锁定整个顺序链。
//
// **注意方向**：数组按 `Assemble` 的**加载顺序**（低优先级 → 高优先级）排列。
// 若按优先级高低排，累积逻辑会反向（首轮就会加载最高层，后续加低层不影响
// 结果），测试失去鉴别力——本测试首版正是这样写错，4 个子用例全红。
func TestAssemblePrecedenceChain(t *testing.T) {
	name := "chain-probe"
	// **加载顺序**：低 → 高。
	layers := []struct {
		desc   string
		dir    func(cwd, home string) string
		source Source
	}{
		{"from-global-agents", func(_, h string) string { return filepath.Join(h, ".agents", "skills") }, SourceGlobalAgents},
		{"from-global-rivet", func(_, h string) string { return filepath.Join(h, ".rivet", "skills") }, SourceGlobalRivet},
		{"from-project-agents", func(c, _ string) string { return filepath.Join(c, ".agents", "skills") }, SourceProjectAgents},
		{"from-project-rivet", func(c, _ string) string { return filepath.Join(c, ".rivet", "skills") }, SourceRivet},
	}

	// 从最低层开始逐层累积——每加一层，生效的应变成新加的这层。
	for upto := 0; upto < len(layers); upto++ {
		t.Run("顶层="+layers[upto].desc, func(t *testing.T) {
			cwd := t.TempDir()
			home := t.TempDir()
			for i := 0; i <= upto; i++ {
				writeSkillFile(t, layers[i].dir(cwd, home), name, layers[i].desc)
			}
			reg, _ := AssembleWithHome(cwd, home)
			got, ok := reg.Get(name)
			if !ok {
				t.Fatal("skill 未加载")
			}
			if got.Description != layers[upto].desc {
				t.Errorf("生效 %q，want %q（已加载 0..%d 层）",
					got.Description, layers[upto].desc, upto)
			}
			if got.Source != layers[upto].source {
				t.Errorf("source = %q, want %q", got.Source, layers[upto].source)
			}
		})
	}
}

// 内置技能必须**最低**优先级——项目同名 skill 覆盖它。
func TestAssembleProjectOverridesBuiltin(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()

	// 取一个真实内置技能名。
	if len(BuiltinSkills) == 0 {
		t.Fatal("无内置技能")
	}
	name := BuiltinSkills[0].Name
	writeSkillFile(t, filepath.Join(cwd, ".rivet", "skills"), name, "overridden-by-project")

	reg, _ := AssembleWithHome(cwd, home)
	got, ok := reg.Get(name)
	if !ok {
		t.Fatal("skill 未加载")
	}
	if got.Description != "overridden-by-project" {
		t.Errorf("内置技能未被项目覆盖：desc=%q", got.Description)
	}
	if got.BuiltIn {
		t.Error("覆盖后 BuiltIn 应为 false")
	}
}

// 内置技能在无任何项目/用户目录时仍可用。
func TestAssembleBuiltinsAlwaysAvailable(t *testing.T) {
	reg, _ := AssembleWithHome(t.TempDir(), t.TempDir())
	for _, b := range BuiltinSkills {
		if _, ok := reg.Get(b.Name); !ok {
			t.Errorf("内置技能 %q 缺失", b.Name)
		}
	}
}

// **反证**：用户级目录不存在时不报错、不产生 errors。
//
// 用户级目录（~/.agents、~/.rivet）在多数机器上不存在——若记为 error，
// 每次启动都会打警告，是噪音。
func TestAssembleMissingDirsSilent(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir() // 空的，无 .agents/.rivet

	reg, res := AssembleWithHome(cwd, home)
	if len(res.Errors) != 0 {
		t.Errorf("目录不存在不应报错，得到：%v", res.Errors)
	}
	// 内置技能仍应加载
	if len(res.Loaded) != len(BuiltinSkills) {
		t.Errorf("loaded = %d, want %d（仅内置）", len(res.Loaded), len(BuiltinSkills))
	}
	_ = reg
}

// `_` 前缀目录被跳过（自动蒸馏草稿不进发现层）——五层都要遵守。
func TestAssembleSkipsUnderscoreEntries(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	for _, d := range []string{
		filepath.Join(cwd, ".rivet", "skills"),
		filepath.Join(cwd, ".agents", "skills"),
		filepath.Join(home, ".rivet", "skills"),
		filepath.Join(home, ".agents", "skills"),
	} {
		writeSkillFile(t, filepath.Join(d, "_drafts"), "draft-probe", "should-be-skipped")
	}
	reg, _ := AssembleWithHome(cwd, home)
	if _, ok := reg.Get("draft-probe"); ok {
		t.Error("`_` 前缀目录内的 skill 不应被加载")
	}
}

// 目录型与扁平型在同一层并存。
func TestAssembleBothShapes(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	dir := filepath.Join(cwd, ".rivet", "skills")
	writeSkillFile(t, dir, "flat-one", "flat")
	writeDirSkill(t, dir, "dir-one", "dir")

	reg, _ := AssembleWithHome(cwd, home)
	for _, n := range []string{"flat-one", "dir-one"} {
		if _, ok := reg.Get(n); !ok {
			t.Errorf("%q 未加载", n)
		}
	}
	// 目录型应有 SkillDir
	d, _ := reg.Get("dir-one")
	if d.SkillDir == "" {
		t.Error("目录型 skill 应设 SkillDir")
	}
	f, _ := reg.Get("flat-one")
	if f.SkillDir != "" {
		t.Error("扁平型 skill 不应设 SkillDir")
	}
}

// home 为空串时只加载内置 + 项目层（不 panic）。
func TestAssembleEmptyHome(t *testing.T) {
	cwd := t.TempDir()
	writeSkillFile(t, filepath.Join(cwd, ".rivet", "skills"), "proj-only", "p")

	reg, res := AssembleWithHome(cwd, "")
	if _, ok := reg.Get("proj-only"); !ok {
		t.Error("项目 skill 未加载")
	}
	if len(res.Errors) != 0 {
		t.Errorf("不应报错：%v", res.Errors)
	}
}

// cwd 为空串时只加载内置 + 用户层（不 panic）。
func TestAssembleEmptyCwd(t *testing.T) {
	home := t.TempDir()
	writeSkillFile(t, filepath.Join(home, ".rivet", "skills"), "global-only", "g")

	reg, res := AssembleWithHome("", home)
	if _, ok := reg.Get("global-only"); !ok {
		t.Error("用户级 skill 未加载")
	}
	if len(res.Errors) != 0 {
		t.Errorf("不应报错：%v", res.Errors)
	}
}

// Loaded 列表应包含各层加载的名字。
func TestAssembleLoadedList(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	writeSkillFile(t, filepath.Join(home, ".agents", "skills"), "g-agents", "1")
	writeSkillFile(t, filepath.Join(home, ".rivet", "skills"), "g-rivet", "2")
	writeSkillFile(t, filepath.Join(cwd, ".agents", "skills"), "p-agents", "3")
	writeSkillFile(t, filepath.Join(cwd, ".rivet", "skills"), "p-rivet", "4")

	_, res := AssembleWithHome(cwd, home)
	want := map[string]bool{"g-agents": false, "g-rivet": false, "p-agents": false, "p-rivet": false}
	for _, n := range res.Loaded {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("loaded 缺 %q：%v", n, res.Loaded)
		}
	}
}
