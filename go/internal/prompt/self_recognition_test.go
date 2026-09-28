package prompt

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// self_recognition_test.go —— 第一百一十二刀 W1：self-recognition 判定。
//
// 对账 `src/prompt/self-recognition.ts` 的 `detectCwdRelation`。
//
// **测试全部用 `t.TempDir()` 自造标记**，不依赖本仓库是否真有 `.rivet/SELF`——
// 否则测试在别的 checkout（或 CI）上会红，那是夹具缺陷不是实现缺陷。

// writeSelfMarker 在 dir 下造出 `.rivet/SELF` 标记。
func writeSelfMarker(t *testing.T, dir string) {
	t.Helper()
	mk := filepath.Join(dir, ".rivet")
	if err := os.MkdirAll(mk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mk, "SELF"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDetectCwdRelationSelf —— ★ V1：有标记 → self。
func TestDetectCwdRelationSelf(t *testing.T) {
	dir := t.TempDir()
	writeSelfMarker(t, dir)

	if got := DetectCwdRelation(dir); got != CwdRelationSelf {
		t.Errorf("有 .rivet/SELF 标记应判 self，实得 %q", got)
	}
}

// TestDetectCwdRelationWorld —— ★ V2：无标记 → world。
func TestDetectCwdRelationWorld(t *testing.T) {
	dir := t.TempDir() // 空目录，无标记

	if got := DetectCwdRelation(dir); got != CwdRelationWorld {
		t.Errorf("无标记应判 world，实得 %q", got)
	}
}

// TestDetectCwdRelationEmptyCwd —— ★ V3：空 cwd → world（不 panic）。
func TestDetectCwdRelationEmptyCwd(t *testing.T) {
	if got := DetectCwdRelation(""); got != CwdRelationWorld {
		t.Errorf("空 cwd 应 fail-toward-world，实得 %q", got)
	}
}

// TestDetectCwdRelationFailTowardWorld —— ★ V4：不可达路径 → world（不 panic）。
//
// **方向判据**（对账 TS 的 catch 注释「never claim a directory as self
// without proof」）：目录不存在、或权限不足，都必须落到 world。
// 误判 self 会让模型以为可以改自己的源码——那是危险方向。
func TestDetectCwdRelationFailTowardWorld(t *testing.T) {
	t.Run("目录不存在", func(t *testing.T) {
		got := DetectCwdRelation(filepath.Join(t.TempDir(), "definitely", "not", "here"))
		if got != CwdRelationWorld {
			t.Errorf("不存在的目录应判 world，实得 %q", got)
		}
	})

	t.Run("标记是目录而非文件也无妨", func(t *testing.T) {
		// 边界：`.rivet/SELF` 若被建成**目录**，os.Stat 仍成功 →
		// 判定 self（对账 TS 的 existsSync 同样对目录返回 true）。
		// 这里钉住该语义，避免将来有人误改成 os.ReadFile 之类的「只认文件」。
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".rivet", "SELF"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := DetectCwdRelation(dir); got != CwdRelationSelf {
			t.Errorf("标记存在（目录形态）应判 self（对账 existsSync 语义），实得 %q", got)
		}
	})

	t.Run("权限不足", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows 的权限模型不同，chmod 0 不阻止 stat")
		}
		if os.Geteuid() == 0 {
			t.Skip("root 不受权限位约束")
		}
		dir := t.TempDir()
		writeSelfMarker(t, dir)
		// 去掉 .rivet 目录的搜索权限 → stat 其下的 SELF 会 EACCES
		if err := os.Chmod(filepath.Join(dir, ".rivet"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, ".rivet"), 0o755) })

		if got := DetectCwdRelation(dir); got != CwdRelationWorld {
			t.Errorf("★ 权限不足应 fail-toward-world（不得误判 self），实得 %q", got)
		}
	})
}

// TestSelfMarkerRelIsStable —— 访问器返回稳定路径。
//
// # ★ 判据订正记录（M3 分诊：我原先的怀疑被证伪，此处如实记录）
//
// 计划里我曾把「`append([]string{cwd}, SelfMarkerPath...)` 会复用底层数组
// 污染包级状态」列为待验证假设 H3，并写了「多次调用结果恒定」的测试。
//
// **M3 变异（把实现改回 append 摊开写法）→ 绿 0**。分诊结论：
// **我的 H3 判断错了**——`append([]string{cwd}, src...)` 中 `[]string{cwd}`
// 是**新分配的**切片，append 只写**目标**的底层数组，**不会写回源** `src`。
// 故该写法无害，M3 是**等价变异**，测试抓不到是正确的。
//
// **但这暴露了原测试的判据是弱代理**：它断言「纯函数多次调用幂等」，
// 而纯函数幂等**恒真**——无论共享状态是否存在（返回常量时更是如此）。
// 订正为**直接观测包级变量**：若将来有人把包级路径改成可变、并被写脏，
// 本用例才真正有判别力。
func TestSelfMarkerRelIsStable(t *testing.T) {
	first := SelfMarkerRel()
	want := filepath.Join(".rivet", "SELF")
	if first != want {
		t.Fatalf("SelfMarkerRel 应为 %q，实得 %q", want, first)
	}
	for i := 0; i < 50; i++ {
		if got := SelfMarkerRel(); got != first {
			t.Fatalf("第 %d 次调用漂移：%q（应为 %q）", i, got, first)
		}
	}
}

// TestSelfMarkerPathNotMutatedByDetection —— ★ 直接观测包级状态（订正后的判据）。
//
// 与上一条的分工：上一条看**访问器输出**，本条看**底层数组本身**。
//
// # 另一处判据订正（编译期发现）
//
// 首版写 `copy(snapshot, selfMarkerPath)` —— **编译失败**：
// `selfMarkerPath` 是 `[2]string`（**数组**，非切片）。
//
// 这顺带说明了一件事：**数组在 Go 里是值类型**——赋值/传参都会整体拷贝，
// 结构上就不可能被外部共享写脏。故「污染包级状态」这个担忧在
// **数组形态下根本不成立**。我把它改成 `[2]string`（而非计划草稿里的切片）
// 正是为了从类型上消除该风险——这条测试因此是**第二道防线**：
// 若将来有人把类型改回切片并引入可变路径，它会红。
func TestSelfMarkerPathNotMutatedByDetection(t *testing.T) {
	snapshot := selfMarkerPath // 数组：值拷贝，天然快照

	for i := 0; i < 100; i++ {
		_ = DetectCwdRelation(t.TempDir())
		_ = SelfMarkerRel()
	}

	for i := range snapshot {
		if selfMarkerPath[i] != snapshot[i] {
			t.Fatalf("★ 包级标记路径被改写：[%d] %q → %q（快照 %v，现状 %v）",
				i, snapshot[i], selfMarkerPath[i], snapshot, selfMarkerPath)
		}
	}
}
