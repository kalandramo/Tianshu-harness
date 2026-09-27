package search

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// relevance_test.go —— 跑题守卫（第九十七刀 · W3）。
//
// 对账 TS `src/tools/web-search/relevance.ts`（113 行）。
//
// # 为什么这一环要重点测
//
// 它是**防止静默出错**的守卫：HTTP 200 + 结构完好 + 内容无关的结果，
// 若被当成答案交给模型，模型无法自我察觉。判据一旦失效**不报错**，
// 只是悄悄放行劣质结果——所以必须有能打红错误实现的对抗用例。

// ── QueryTokenGroups ────────────────────────────────────────────────────

func TestQueryTokenGroups(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  [][]string
	}{
		{
			name:  "纯拉丁整词小写",
			query: "Go Runtime",
			want:  [][]string{{"go"}, {"runtime"}},
		},
		{
			name:  "中文按 bigram",
			query: "杭州西湖",
			want:  [][]string{{"杭州", "州西", "西湖"}},
		},
		{
			name:  "数字与汉字混合参与 bigram",
			query: "7月事件",
			want:  [][]string{{"7月", "月事", "事件"}},
		},
		{
			name:  "长度 <2 的段丢弃",
			query: "a 好的 b 测试",
			want:  [][]string{{"好的"}, {"测试"}},
		},
		{
			name:  "site: 操作符整段剔除",
			query: "关键词 site:example.com",
			want:  [][]string{{"关键", "键词"}},
		},
		{
			name:  "site: 剔除后不影响前后词",
			query: "foo site:x.com bar",
			want:  [][]string{{"foo"}, {"bar"}},
		},
		{
			name:  "标点作分隔",
			query: "rust,cargo;tokio",
			want:  [][]string{{"rust"}, {"cargo"}, {"tokio"}},
		},
		{
			name:  "中英混排保持查询顺序",
			query: "Go 语言",
			want:  [][]string{{"go"}, {"语言"}},
		},
		{
			name:  "空查询",
			query: "",
			want:  [][]string{},
		},
		{
			name:  "只有标点",
			query: " , ; ",
			want:  [][]string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := QueryTokenGroups(c.query)
			if len(got) != len(c.want) {
				t.Fatalf("分组数 = %d，期望 %d（实得 %#v）", len(got), len(c.want), got)
			}
			for i := range c.want {
				if len(got[i]) != len(c.want[i]) {
					t.Fatalf("组 %d 词元数 = %d，期望 %d（实得 %#v）", i, len(got[i]), len(c.want[i]), got[i])
				}
				for j := range c.want[i] {
					if got[i][j] != c.want[i][j] {
						t.Errorf("组 %d 词元 %d = %q，期望 %q", i, j, got[i][j], c.want[i][j])
					}
				}
			}
		})
	}
}

func TestQueryTokensFlattens(t *testing.T) {
	got := QueryTokens("Go 语言")
	want := []string{"go", "语言"}
	if len(got) != len(want) {
		t.Fatalf("实得 %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("实得 %#v，期望 %#v", got, want)
		}
	}
}

// ── LooksOffTopic 主判据 ────────────────────────────────────────────────

func TestLooksOffTopicEmptyResults(t *testing.T) {
	// 空结果集不判定——「没有结果」是 chain 的既有路径
	if LooksOffTopic("any query", nil) {
		t.Error("空结果集不应判跑题")
	}
	if LooksOffTopic("any query", []Result{}) {
		t.Error("空结果集不应判跑题")
	}
}

func TestLooksOffTopicNoTokens(t *testing.T) {
	// 查询提不出词元 → 无判据可依时放行（保守）
	results := []Result{{Title: "Something", Snippet: "else"}}
	if LooksOffTopic(" , . ", results) {
		t.Error("查询无词元时应放行")
	}
}

// TestLooksOffTopicSingleWordZeroOverlap —— 单查询词走零重叠判据。
func TestLooksOffTopicSingleWordZeroOverlap(t *testing.T) {
	query := "kubernetes"

	// 全批零命中 → 判跑题
	off := []Result{
		{Title: "红烧肉做法", Snippet: "五花肉切块"},
		{Title: "天气预报", Snippet: "明日多云"},
	}
	if !LooksOffTopic(query, off) {
		t.Error("单查询词全批零命中应判跑题")
	}

	// 任一命中 → 放行
	on := []Result{
		{Title: "红烧肉做法", Snippet: "与容器无关"},
		{Title: "Kubernetes 入门", Snippet: "集群编排"},
	}
	if LooksOffTopic(query, on) {
		t.Error("单查询词任一命中应放行")
	}
}

// TestLooksOffTopicMultiWordRequiresCoverage —— **本守卫的核心判据**。
//
// 多查询词时，要求覆盖 **≥2 个不同查询词** 的结果**过半数**才放行。
// 单个泛词命中骗不过判据——这是 TS 注释里「降级泛结果」那一类的防线。
func TestLooksOffTopicMultiWordRequiresCoverage(t *testing.T) {
	// 复现 TS 注释里的真实退化场景：`美国 AI 实验室 出逃 事件 7月 智能体`
	// 退化结果是清一色「美国」百科/领事馆页面——只覆盖了「美国」这一个词。
	query := "美国 AI 实验室 出逃 事件 7月 智能体"
	degraded := []Result{
		{Title: "美国 - 维基百科", Snippet: "美利坚合众国，简称美国"},
		{Title: "美国驻华大使馆", Snippet: "美国国务院下属机构"},
		{Title: "美国历史简介", Snippet: "美国建国于 1776 年"},
		{Title: "美国旅游攻略", Snippet: "美国五十个州"},
	}
	if !LooksOffTopic(query, degraded) {
		t.Error("只覆盖单个泛词的降级结果应判跑题（这是本判据存在的理由）")
	}

	// 真正相关：多数结果覆盖 ≥2 个查询词
	relevant := []Result{
		{Title: "美国 AI 实验室研究人员出逃事件", Snippet: "7月发生的智能体出逃事件"},
		{Title: "AI 实验室出逃事件始末", Snippet: "美国多家实验室 7月 报告"},
		{Title: "智能体出逃：7月事件复盘", Snippet: "美国 AI 实验室"},
	}
	if LooksOffTopic(query, relevant) {
		t.Error("多数结果覆盖 ≥2 个查询词时应放行")
	}
}

// TestLooksOffTopicHalfThreshold —— 恰好半数的边界（`multiCovered*2 <= len` 时判跑题）。
func TestLooksOffTopicHalfThreshold(t *testing.T) {
	query := "alpha beta"
	// 2 条中 1 条覆盖两词 → 1*2 <= 2 → 判跑题
	half := []Result{
		{Title: "alpha beta both", Snippet: ""},
		{Title: "alpha only", Snippet: ""},
	}
	if !LooksOffTopic(query, half) {
		t.Error("恰好半数覆盖应判跑题（判据是「过半数」）")
	}

	// 3 条中 2 条覆盖两词 → 2*2=4 > 3 → 放行
	three := []Result{
		{Title: "alpha beta", Snippet: ""},
		{Title: "beta alpha", Snippet: ""},
		{Title: "gamma", Snippet: ""},
	}
	if LooksOffTopic(query, three) {
		t.Error("过半数覆盖应放行")
	}
}

// TestLooksOffTopicBigramSelfOverlapGuard —— **防「单词虚高」**。
//
// TS 注释点名：同一个查询词内的多枚 bigram 只计一词——否则「量子计算」这一个词
// 靠 量子/子计/计算 三枚 bigram 就能自重叠虚高，把只含该词的结果送过闸门。
func TestLooksOffTopicBigramSelfOverlapGuard(t *testing.T) {
	// 查询是两个中文词 → 需要覆盖 ≥2 个词
	query := "量子计算 论文"
	// 结果只含「量子计算」相关（覆盖第 1 个词，但覆盖不了「论文」）
	onlyFirst := []Result{
		{Title: "量子计算入门", Snippet: "量子比特与量子门"},
		{Title: "量子计算原理", Snippet: "量子算法"},
		{Title: "量子计算课程", Snippet: "量子力学基础"},
		{Title: "量子计算应用", Snippet: "量子优势"},
	}
	if !LooksOffTopic(query, onlyFirst) {
		t.Error("只覆盖单个中文查询词（哪怕命中多枚 bigram）应判跑题——bigram 不得自重叠虚高")
	}
}

// TestLooksOffTopicIgnoresURL —— URL 不参与匹配。
//
// slug 命中是噪声（`/hangzhou-xihu-menpiao` 可能挂在完全无关的垃圾站上）。
func TestLooksOffTopicIgnoresURL(t *testing.T) {
	query := "alpha beta"
	// title/snippet 完全无关，但 URL 含两个词
	results := []Result{
		{Title: "无关页面", URL: "https://spam.example/alpha-beta", Snippet: "无关内容"},
	}
	if !LooksOffTopic(query, results) {
		t.Error("URL 命中不应让守卫放行——URL 不参与匹配")
	}
}

func TestLooksOffTopicCaseInsensitive(t *testing.T) {
	query := "KUBERNETES"
	results := []Result{{Title: "kubernetes 集群", Snippet: ""}}
	if LooksOffTopic(query, results) {
		t.Error("匹配应大小写不敏感")
	}
}

// ── Chain ───────────────────────────────────────────────────────────────

// stubBackend 是测试用后端。
type stubBackend struct {
	name      string
	available bool
	results   []Result
	err       error
	delay     time.Duration
	calls     int
}

func (s *stubBackend) Name() string      { return s.name }
func (s *stubBackend) IsAvailable() bool { return s.available }

func (s *stubBackend) Search(ctx context.Context, query string, count int) ([]Result, error) {
	s.calls++
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.results, nil
}

func TestChainFirstAvailableWins(t *testing.T) {
	a := &stubBackend{name: "a", available: true, results: []Result{{Title: "from a"}}}
	b := &stubBackend{name: "b", available: true, results: []Result{{Title: "from b"}}}
	res := RunBackendChain(context.Background(), []Backend{a, b}, "q", 10, 1000)

	if res.Backend != "a" {
		t.Errorf("首个可用应胜出，实得 %q", res.Backend)
	}
	if b.calls != 0 {
		t.Error("胜出后应短路，b 不该被调用")
	}
}

func TestChainSkipsUnavailableWithoutError(t *testing.T) {
	unavail := &stubBackend{name: "nokey", available: false}
	avail := &stubBackend{name: "ok", available: true, results: []Result{{Title: "hit"}}}
	res := RunBackendChain(context.Background(), []Backend{unavail, avail}, "q", 10, 1000)

	if res.Backend != "ok" {
		t.Errorf("实得 %q", res.Backend)
	}
	if unavail.calls != 0 {
		t.Error("不可用后端不该被调用")
	}
	// 不可用**不算错误**（对账 TS：静默跳过）
	if len(res.Errors) != 0 {
		t.Errorf("不可用后端不该记错误，实得 %#v", res.Errors)
	}
}

func TestChainRecordsEmptyAndError(t *testing.T) {
	empty := &stubBackend{name: "empty", available: true, results: nil}
	failing := &stubBackend{name: "fail", available: true, err: errors.New("HTTP 503")}
	winner := &stubBackend{name: "win", available: true, results: []Result{{Title: "ok"}}}

	res := RunBackendChain(context.Background(), []Backend{empty, failing, winner}, "q", 10, 1000)
	if res.Backend != "win" {
		t.Fatalf("实得 %q", res.Backend)
	}
	if len(res.Errors) != 2 {
		t.Fatalf("应记录 2 条错误，实得 %#v", res.Errors)
	}
	if res.Errors[0].Message != NoResultsError {
		t.Errorf("空结果应记为 no results，实得 %q", res.Errors[0].Message)
	}
	if !strings.Contains(res.Errors[1].Message, "503") {
		t.Errorf("失败应保留原文，实得 %q", res.Errors[1].Message)
	}
}

// TestChainOffTopicFallsThroughAndRetainsFirst —— **跑题必须落空并记住首个批次**。
func TestChainOffTopicFallsThroughAndRetainsFirst(t *testing.T) {
	offA := &stubBackend{name: "off-a", available: true,
		results: []Result{{Title: "完全无关甲", Snippet: "x"}}}
	offB := &stubBackend{name: "off-b", available: true,
		results: []Result{{Title: "完全无关乙", Snippet: "y"}}}

	res := RunBackendChain(context.Background(), []Backend{offA, offB}, "zzzz qqqq", 10, 1000)

	if res.Backend != "" || len(res.Results) != 0 {
		t.Errorf("跑题批次不应胜出，实得 backend=%q results=%d", res.Backend, len(res.Results))
	}
	if res.OffTopicFallback == nil {
		t.Fatal("应保留跑题兜底批次")
	}
	// **链序优先**：只保留第一个，后续不得覆盖
	if res.OffTopicFallback.Backend != "off-a" {
		t.Errorf("应保留链序第一个跑题批次，实得 %q", res.OffTopicFallback.Backend)
	}
	// 两条都记为跑题错误
	if len(res.Errors) != 2 {
		t.Fatalf("实得 %#v", res.Errors)
	}
	for _, e := range res.Errors {
		if e.Message != OffTopicError {
			t.Errorf("应记为跑题错误，实得 %q", e.Message)
		}
	}
}

// TestChainOffTopicThenWinner —— 跑题后仍有后端能给出相关结果 → 正常胜出。
func TestChainOffTopicThenWinner(t *testing.T) {
	off := &stubBackend{name: "off", available: true,
		results: []Result{{Title: "无关甲", Snippet: "x"}}}
	win := &stubBackend{name: "win", available: true,
		results: []Result{{Title: "alpha beta 都对", Snippet: "alpha beta"}}}

	res := RunBackendChain(context.Background(), []Backend{off, win}, "alpha beta", 10, 1000)
	if res.Backend != "win" {
		t.Errorf("相关结果应胜出，实得 %q", res.Backend)
	}
	if res.OffTopicFallback != nil {
		t.Error("已有胜出时不该保留兜底（对账 TS 的早返回）")
	}
}

// TestChainPerBackendTimeout —— 单后端超时被记为该后端的失败，链继续。
func TestChainPerBackendTimeout(t *testing.T) {
	slow := &stubBackend{name: "slow", available: true, delay: 500 * time.Millisecond,
		results: []Result{{Title: "too late"}}}
	fast := &stubBackend{name: "fast", available: true, results: []Result{{Title: "quick"}}}

	start := time.Now()
	res := RunBackendChain(context.Background(), []Backend{slow, fast}, "q", 10, 50)
	elapsed := time.Since(start)

	if res.Backend != "fast" {
		t.Errorf("超时后应继续到 fast，实得 %q", res.Backend)
	}
	if elapsed > 400*time.Millisecond {
		t.Errorf("超时应快速失败，耗时 %v", elapsed)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "timed out") {
		t.Errorf("应记超时错误，实得 %#v", res.Errors)
	}
}

// TestChainNilContext —— nil ctx 不 panic（便于调用方省略）。
func TestChainNilContext(t *testing.T) {
	b := &stubBackend{name: "b", available: true, results: []Result{{Title: "ok"}}}
	res := RunBackendChain(nil, []Backend{b}, "q", 10, 1000)
	if res.Backend != "b" {
		t.Errorf("nil ctx 应正常工作，实得 %q", res.Backend)
	}
}

func TestDescribeError(t *testing.T) {
	if got := describeError(context.DeadlineExceeded, 15000); got != "timed out after 15s" {
		t.Errorf("实得 %q", got)
	}
	if got := describeError(context.Canceled, 1000); got != "canceled" {
		t.Errorf("实得 %q", got)
	}
	if got := describeError(errors.New("HTTP 500"), 1000); got != "HTTP 500" {
		t.Errorf("实得 %q", got)
	}
}
