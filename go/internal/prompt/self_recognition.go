// self_recognition.go —— 自我识别（self-recognition）。
//
// 对账 `src/prompt/self-recognition.ts`。
//
// 天枢是终端编码智能体。多数时候他站在开发者的仓库里（**世界的项目**），
// 那里项目正确地是外部的，他作为携带自身的访客（emissary 形态）。
// 只有站在自己的源码里（**他的身体**）时，cwd 才是他自己
// （home / 自我演化形态）。
//
// **自我身份是「声明」而非「猜测」**（逐字对账 TS 注释）：
// `.rivet/SELF` 标记只存在于天枢的真实源码中。没有它，cwd 就是世界。
// 这让生产环境（开发者从不拥有该标记）恒为 `world`，
// 而使自我演化成为一个**只在真身上激活的特权模式**。
//
// **为什么可以进 frozen 前缀**（前缀缓存安全）：判定结果对给定 cwd 在
// 一个会话内是**常量**（与 `rivetMd` 同类），不是逐轮变化的量。
// 这与 `planModeState`（会话中途可翻转 → 只能进动态 appendix）是
// **关键区别**。TS 源码对此有明文背书（`self-recognition.ts` 的文件头注释）。
package prompt

import (
	"os"
	"path/filepath"
)

// SelfMarkerPath 是声明「此目录是天枢自身身体」的标记路径（相对 cwd）。
//
// 对账 TS 的 `SELF_MARKER_PATH = ['.rivet', 'SELF'] as const`。
//
// **为什么导出**：装配层与测试需要引用它来造/查标记。
// **为什么不可变意图**：它是常量语义；Go 的 `var` 切片无法标 const，
// 故此处用**包级私有后备 + 访问器**避免调用方误改（见 SelfMarkerRel）。
var selfMarkerPath = [2]string{".rivet", "SELF"}

// SelfMarkerRel 返回标记的**相对路径**（用 `filepath.Join` 拼好）。
//
// **为什么给访问器而非直接暴露切片**（本刀的一个实现要点）：暴露可变切片
// 会让调用方有机会 `append(SelfMarkerPath, ...)` —— Go 的 `append` 在
// 容量足够时会**复用底层数组**，从而污染包级状态。返回拼好的字符串
// 从类型上消除这个风险。
//
// 这也是本计划 H3 记录的坑：初版草稿写的是
// `filepath.Join(append([]string{cwd}, SelfMarkerPath...)...)` ——
// 即使外层做了 `[]string{cwd}` 包装，这种「把包级切片摊进 append」的写法
// 在多处调用时仍是隐患。改为直接拼字面量，最稳。
func SelfMarkerRel() string {
	return filepath.Join(selfMarkerPath[0], selfMarkerPath[1])
}

// CwdRelation 的两个取值。
//
// **为什么是裸字符串常量而非自定义类型**：`VolatileContext.CwdRelation` 已是
// `string`（`volatile.go`）且有既有消费分支按字符串比较。改成具名类型会牵动
// **已对账**的 `volatile.go` 及其 oracle。**不为类型美感动已对账的代码**——
// 这是本刀的一个刻意取舍（对账 TS 的联合类型 `'self' | 'world'` 在 Go 侧
// 用字符串表达是可接受的既有约定）。
const (
	// CwdRelationSelf 表示 cwd 是天枢自身的源码（真身）。
	CwdRelationSelf = "self"
	// CwdRelationWorld 表示 cwd 是外部项目（访客形态）。
	CwdRelationWorld = "world"
)

// DetectCwdRelation 判定 cwd 是天枢自身还是外部项目。
//
// 对账 TS `detectCwdRelation`（`src/prompt/self-recognition.ts`）：
//
//	try {
//	  return existsSync(join(cwd, ...SELF_MARKER_PATH)) ? 'self' : 'world'
//	} catch {
//	  // Filesystem unreachable / permission denied → treat as world (guest).
//	  // Fail toward 'world': never claim a directory as self without proof.
//	  return 'world'
//	}
//
// # fail-toward-world（方向很重要）
//
// 文件系统不可达 / 权限不足 / cwd 为空 → 一律返回 `world`。
// **误判 `self` 会让模型以为可以改自己的源码**——那是危险方向；
// 误判 `world` 只是少一条提示。故一律朝安全侧失败。
//
// 注意 Go 的 `os.Stat` 与 JS 的 `existsSync` 语义差异：前者返回 `err`
// （含「不存在」与「权限拒绝」），后者返回 bool。此处**只要 err != nil
// 就返回 world**，把两类都归入安全侧——这正对账 TS 的 `catch` 分支
// （TS 的 existsSync 对权限错误也返回 false，行为一致）。
func DetectCwdRelation(cwd string) string {
	if cwd == "" {
		// 空 cwd 无法判定 → 安全侧。
		return CwdRelationWorld
	}
	if _, err := os.Stat(filepath.Join(cwd, selfMarkerPath[0], selfMarkerPath[1])); err == nil {
		return CwdRelationSelf
	}
	return CwdRelationWorld
}
