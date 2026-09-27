package lsp

// Navigator 是供 `internal/tools` 消费的 LSP 能力面。
//
// # 为什么要有这一层适配
//
// `internal/tools` 定义了 `LspNavigator` 接口与自己的 `LspLocation` 类型
// **刻意不 import 本包**（详见 tools/lsptools.go 的说明：LSP 的装配属运行时
// 设施，由上层注入；且避免工具层依赖子系统实现）。
//
// 于是本包提供 Navigator 作为**实现侧**：它满足 `tools.LspNavigator` 的
// 方法集（结构相同即可，Go 的接口是隐式的），并在返回位置时把本包的
// `Location` 转成 tools 侧的同形类型。
//
// **为什么不做成「tools 直接 import lsp」**：那会让 `tools` 包（工具内核）
// 依赖语言服务器子系统——方向是反的（子系统该依赖契约，而非内核依赖子系统）。
type Navigator struct {
	multi *multiManager
}

// NewNavigator 造一个 Navigator（cwd 是工作目录；懒启动，无 immediate spawn）。
func NewNavigator(cwd string) *Navigator {
	return &Navigator{multi: newMultiManager(cwd, nil)}
}

// NewNavigatorWith 供测试/定制注入（which 与 spawn 缝）。
func NewNavigatorWith(cwd string, opts *multiManagerOptions) *Navigator {
	return &Navigator{multi: newMultiManager(cwd, opts)}
}

// Initialize 完成装配。multi-manager 的 initialize 是空操作（懒启动），
// 保留它是为了对齐 TS 的 `initializeLsp()` 调用形态与错误面。
func (n *Navigator) Initialize() error {
	if n == nil || n.multi == nil {
		return nil
	}
	return n.multi.Initialize()
}

// IsReady 报告是否有任何已安装的语言服务器。
func (n *Navigator) IsReady() bool {
	return n != nil && n.multi != nil && n.multi.IsReady()
}

// SupportsDefinition 报告是否支持定义跳转（multi 层语义：有 server 装即可）。
func (n *Navigator) SupportsDefinition() bool {
	return n != nil && n.multi != nil && n.multi.SupportsDefinition()
}

// SupportsReferences 报告是否支持引用查找。
func (n *Navigator) SupportsReferences() bool {
	return n != nil && n.multi != nil && n.multi.SupportsReferences()
}

// Locations 是本包的返回类型别名（便于调用方表达）。
type Locations = []Location

// GotoDefinitionResult 的结构与 `tools.LspLocation` 一致，
// 故 tools 侧以**结构相同的切片类型**接收即可（Go 接口的隐式满足要求
// 方法签名完全一致——故此处必须返回 tools 侧的类型，见下方说明）。

// Navigator 的方法签名说明
//
// `tools.LspNavigator` 要求：
//
//	GotoDefinition(filePath string, line, column int) ([]tools.LspLocation, error)
//
// 而本包若返回 `[]lsp.Location`，**不满足**该接口（Go 的接口要求签名
// 完全一致，含命名类型）。故有两个选择：
//
//	① 让 tools 侧把 `LspLocation` 定义成类型别名 → 仍指向 lsp 包（引入依赖）
//	② 在装配层包一层适配器，做字段拷贝
//
// 本包取 **③：让 `tools.LspLocation` 与 `lsp.Location` 都只是「同形的普通
// struct」，由装配层（cmd/tianshu）写一个薄适配器**——两包互不依赖，
// 转换代码集中在一处（见 cmd/tianshu/main.go 的 lspNavigatorAdapter）。
//
// 这样做的代价是一次字段拷贝（每个位置 3 个 int + 1 个 string），
// 收益是依赖方向正确且两侧可独立演化。

// GotoDefinition 返回定义位置（本包类型）。
func (n *Navigator) GotoDefinition(filePath string, line, column int) ([]Location, error) {
	if n == nil || n.multi == nil {
		return nil, nil
	}
	return n.multi.GotoDefinition(filePath, line, column)
}

// FindReferences 返回引用位置（本包类型）。
func (n *Navigator) FindReferences(filePath string, line, column int) ([]Location, error) {
	if n == nil || n.multi == nil {
		return nil, nil
	}
	return n.multi.FindReferences(filePath, line, column)
}

// ChangeFile 通知所属 server 文件已改（供工具管线在写工具后调用）。
func (n *Navigator) ChangeFile(filePath string) {
	if n == nil || n.multi == nil {
		return
	}
	n.multi.ChangeFile(filePath)
}

// Dispose 释放全部语言服务器。
func (n *Navigator) Dispose() {
	if n == nil || n.multi == nil {
		return
	}
	n.multi.Dispose()
}
