package tools

import "testing"

// TestShouldRunDiagnostics —— 对账 TS `shouldRunDiagnostics`（client.ts:265-277）。
//
// 判别力：改成「含 apply_patch」或「忽略 hasServer」都会红。
func TestShouldRunDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tool     string
		filePath string
		hasSrv   bool
		want     bool
	}{
		{"write_file + server", "write_file", "a.go", true, true},
		{"edit_file + server", "edit_file", "a.go", true, true},
		{"write_file 无 server", "write_file", "a.go", false, false},
		{"write_file 无路径", "write_file", "", true, false},
		// ★ 这三个是「有意收窄」——只认 write_file/edit_file
		{"apply_patch 不触发", "apply_patch", "a.go", true, false},
		{"hash_edit 不触发", "hash_edit", "a.go", true, false},
		{"read_file 不触发", "read_file", "a.go", true, false},
		{"bash 不触发", "bash", "a.go", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldRunDiagnostics(tc.tool, tc.filePath, tc.hasSrv); got != tc.want {
				t.Errorf("ShouldRunDiagnostics(%q,%q,%v) = %v，期望 %v",
					tc.tool, tc.filePath, tc.hasSrv, got, tc.want)
			}
		})
	}
}

// TestWriteToolNames_CoversChangeFileSurface —— changeFile 通知面比诊断面宽。
//
// 断言两者**有意不同**：apply_patch 在通知面内、在诊断面外。
// 若有人把两者统一，本用例会红——那时应确认那是有意的语义变更。
func TestWriteToolNames_CoversChangeFileSurface(t *testing.T) {
	names := WriteToolNames()
	for _, want := range []string{"write_file", "edit_file", "apply_patch", "hash_edit", "apply_edit"} {
		if !names[want] {
			t.Errorf("changeFile 通知面应含 %q", want)
		}
	}
	// ★ 诊断面刻意更窄：apply_patch 在通知面内
	if !names["apply_patch"] {
		t.Error("apply_patch 应在通知面（让它清理过期诊断）")
	}
	if ShouldRunDiagnostics("apply_patch", "a.go", true) {
		t.Error("apply_patch 不该触发诊断（与通知面不同，这是 TS 原样语义）")
	}
}
