package tools

import (
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
)

// Options 是默认注册表的装配选项。
type Options struct {
	// Cwd 是工作目录。
	Cwd string
	// Grants 是越界路径授权判定。
	Grants pathsafe.GrantChecker
	// Extra 是额外注册的工具（装配注入层）。
	Extra []Tool
}

// NewDefaultRegistry 装配默认工具集。
//
// 对账 src/tools/default-registry.ts 的 kernel 层。Go 版先实现内核子集——
// 完整 44 个工具是分波目标，此处只收「读写检索执行」四类的基础工具。
//
// 装配顺序不影响行为（Definitions() 按名升序输出，保证请求体字节稳定），
// 但保持与 TS 相近的分组便于对账。
func NewDefaultRegistry(opts Options) *Registry {
	r := NewRegistry()
	cwd := opts.Cwd

	// ── 文件读写 ──
	r.Register(ReadFile(cwd, opts.Grants))
	r.Register(WriteFile(cwd, opts.Grants))
	r.Register(EditFile(cwd, opts.Grants))
	r.Register(HashEdit(cwd, opts.Grants))
	r.Register(ApplyPatch(cwd, opts.Grants))

	// ── 检索 ──
	r.Register(Glob(cwd))
	r.Register(Grep(cwd))
	// read_section：artifact 召回路径（大工具结果被拦截后按区段取回）。
	// 对账 TS 的 read-section.ts——属 read 类，minimal preset 也含。
	r.Register(ReadSection(cwd, opts.Grants))

	// ── 执行 ──
	r.Register(Bash(cwd))
	r.Register(RunTests(cwd))

	// ── 任务 ──
	r.Register(Todo())

	// ── 装配注入 ──
	for _, t := range opts.Extra {
		r.Register(t)
	}
	return r
}

// Filter 按白名单过滤注册表（对应 TS 的 filterToolRegistry）。
//
// 白名单里出现未注册的工具名时**失败**而非静默忽略——静默会让拼错的
// authority 白名单退化为空集却无人察觉。
func Filter(src *Registry, allowed []string) (*Registry, error) {
	out := NewRegistry()
	for _, name := range allowed {
		t, ok := src.Get(name)
		if !ok {
			return nil, &UnknownAllowlistEntry{Name: name}
		}
		out.Register(t)
	}
	return out, nil
}

// UnknownAllowlistEntry 表示白名单引用了未注册的工具。
type UnknownAllowlistEntry struct{ Name string }

func (e *UnknownAllowlistEntry) Error() string {
	return "Cannot allowlist unknown tool: " + e.Name
}
