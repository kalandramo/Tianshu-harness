package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// T0 与 gen-oracle.ts 的 T0 一致（固定时间戳，保证 oracle 可复现）。
const T0 int64 = 1700000000000

// claimOracleEntry 是 oracle 里一项。
type claimOracleEntry struct {
	Render            *string            `json:"render"`
	Checkpoint        *ClaimSnapshotJSON `json:"checkpoint"`
	CheckpointWithSeq *ClaimSnapshotJSON `json:"checkpointWithSeq"`
	StatusCounts      *ClaimStatusCounts `json:"statusCounts"`
	Loaded            []ContextClaim     `json:"loaded"`
	Promotion         *string            `json:"promotion"`
	Statuses          []struct {
		Status   string `json:"status"`
		Eligible bool   `json:"eligible"`
	} `json:"statuses"`
	Proposals []struct {
		Proposal ClaimProposal `json:"proposal"`
		ID       string        `json:"id"`
	} `json:"proposals"`
	Checks []struct {
		Claim  ContextClaim `json:"claim"`
		Path   string       `json:"path"`
		Result bool         `json:"result"`
	} `json:"checks"`
	Anchors []struct {
		Anchor   ContextAnchor `json:"anchor"`
		Proposal ClaimProposal `json:"proposal"`
	} `json:"anchors"`
}

// ClaimSnapshotJSON 是 oracle 里的快照形态。
type ClaimSnapshotJSON struct {
	Version      int            `json:"version"`
	CreatedAt    int64          `json:"createdAt"`
	LastEventSeq *int64         `json:"lastEventSeq"`
	Claims       []ContextClaim `json:"claims"`
}

// claimCase 是 cases.json 里一项。
type claimCase struct {
	Claims   []ContextClaim     `json:"claims"`
	Claim    *ContextClaim      `json:"claim"`
	Snapshot *ClaimSnapshotJSON `json:"snapshot"`
	Statuses []struct {
		Status   string `json:"status"`
		Eligible bool   `json:"eligible"`
	} `json:"statuses"`
	Proposals []struct {
		Proposal ClaimProposal `json:"proposal"`
		ID       string        `json:"id"`
	} `json:"proposals"`
	Checks []struct {
		Claim  ContextClaim `json:"claim"`
		Path   string       `json:"path"`
		Result bool         `json:"result"`
	} `json:"checks"`
	Anchors []struct {
		Anchor   ContextAnchor `json:"anchor"`
		Proposal ClaimProposal `json:"proposal"`
	} `json:"anchors"`
}

func loadClaimOracle(t *testing.T) map[string]claimOracleEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claims", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败（先跑 node_modules/.bin/tsx go/testdata/claims/gen-oracle.ts）：%v", err)
	}
	var out map[string]claimOracleEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(out) == 0 {
		t.Fatal("oracle 为空")
	}
	return out
}

func loadClaimCases(t *testing.T) map[string]claimCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claims", "cases.json"))
	if err != nil {
		t.Fatalf("读 cases 失败：%v", err)
	}
	var out map[string]claimCase
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 cases 失败：%v", err)
	}
	return out
}

// TestClaimIDDerivationParity —— **ID 派生对账**（sha256 + 文本归一化 + 键序）。
func TestClaimIDDerivationParity(t *testing.T) {
	oracle := loadClaimOracle(t)
	cases := loadClaimCases(t)

	o, ok := oracle["id_derivation"]
	if !ok {
		t.Fatal("oracle 缺 id_derivation")
	}
	c := cases["id_derivation"]

	if len(o.Proposals) != len(c.Proposals) {
		t.Fatalf("提案数不符：oracle=%d cases=%d", len(o.Proposals), len(c.Proposals))
	}

	for i, want := range o.Proposals {
		got := CreateClaimFromProposal(c.Proposals[i].Proposal)
		if got.ID != want.ID {
			t.Errorf("[%d] text=%q：ID 不符\nwant %q\ngot  %q",
				i, c.Proposals[i].Proposal.Text, want.ID, got.ID)
		}
	}
}

// TestRenderActiveClaimsParity —— **prompt 渲染逐字节对账**。
func TestRenderActiveClaimsParity(t *testing.T) {
	oracle := loadClaimOracle(t)
	cases := loadClaimCases(t)

	for name, entry := range oracle {
		if entry.Render == nil {
			continue
		}
		t.Run(name, func(t *testing.T) {
			spec, ok := cases[name]
			if !ok {
				t.Skipf("%s 无对应 cases", name)
			}
			got := RenderActiveClaimsBlockAt(spec.Claims, T0)
			if got != *entry.Render {
				t.Errorf("渲染不符\nwant: %q\ngot:  %q", *entry.Render, got)
			}
		})
	}
}

// TestCheckpointParity —— 快照导出对账。
func TestCheckpointParity(t *testing.T) {
	oracle := loadClaimOracle(t)
	cases := loadClaimCases(t)

	for name, entry := range oracle {
		if entry.Checkpoint == nil {
			continue
		}
		t.Run(name, func(t *testing.T) {
			spec := cases[name]
			got := CheckpointClaims(spec.Claims, T0)

			if got.Version != entry.Checkpoint.Version {
				t.Errorf("version：want %d got %d", entry.Checkpoint.Version, got.Version)
			}
			if got.CreatedAt != entry.Checkpoint.CreatedAt {
				t.Errorf("createdAt：want %d got %d", entry.Checkpoint.CreatedAt, got.CreatedAt)
			}
			if len(got.Claims) != len(entry.Checkpoint.Claims) {
				t.Fatalf("claim 数：want %d got %d", len(entry.Checkpoint.Claims), len(got.Claims))
			}
			for i := range got.Claims {
				if got.Claims[i].ID != entry.Checkpoint.Claims[i].ID {
					t.Errorf("[%d] ID：want %q got %q", i, entry.Checkpoint.Claims[i].ID, got.Claims[i].ID)
				}
			}
			// 带 seq 版本
			if entry.CheckpointWithSeq != nil {
				gotSeq := CheckpointClaims(spec.Claims, T0)
				if entry.CheckpointWithSeq.LastEventSeq != nil {
					gotSeq.LastEventSeq = *entry.CheckpointWithSeq.LastEventSeq
					gotSeq.HasEventSeq = true
				}
				// seq 只影响元数据，claim 集不变——验证一致性
				if len(gotSeq.Claims) != len(entry.CheckpointWithSeq.Claims) {
					t.Errorf("带 seq 版本的 claim 数不符")
				}
			}
		})
	}
}

// TestLoadSnapshotVersionMismatch —— version 不匹配返回空。
func TestLoadSnapshotVersionMismatch(t *testing.T) {
	oracle := loadClaimOracle(t)
	cases := loadClaimCases(t)

	entry, ok := oracle["load_snapshot_version_mismatch"]
	if !ok {
		t.Fatal("oracle 缺 load_snapshot_version_mismatch")
	}
	c := cases["load_snapshot_version_mismatch"]
	if c.Snapshot == nil {
		t.Fatal("cases 缺 snapshot")
	}

	got := LoadClaimSnapshot(ClaimSnapshot{
		Version: c.Snapshot.Version,
		Claims:  c.Snapshot.Claims,
	}, T0)

	if len(got) != len(entry.Loaded) {
		t.Errorf("version 不匹配应返回空（want %d 条，got %d 条）", len(entry.Loaded), len(got))
	}
}

// TestEligibleMatrixParity —— isPromptEligibleClaim 全状态矩阵。
func TestEligibleMatrixParity(t *testing.T) {
	oracle := loadClaimOracle(t)
	cases := loadClaimCases(t)

	o, ok := oracle["eligible_matrix"]
	if !ok {
		t.Fatal("oracle 缺 eligible_matrix")
	}
	_ = cases

	for _, want := range o.Statuses {
		claim := ContextClaim{
			Status:    ContextClaimStatus(want.Status),
			CreatedAt: T0,
		}
		got := IsPromptEligibleClaim(claim, T0)
		if got != want.Eligible {
			t.Errorf("status=%s：want eligible=%v got %v", want.Status, want.Eligible, got)
		}
	}
}

// TestPromotionParity —— 晋升判定对账（**含 null 用例**）。
//
// **关键**：oracle 里 `promotion: null` 的用例必须也被断言——否则「不该晋升」
// 这条语义完全没被覆盖。首版就漏了：`entry.Promotion == nil` 直接 continue，
// 导致 7 分钟阈值用例形同虚设，阈值变异红 0。
//
// **实现**：改用「该用例的 claim 是否存在于 cases 且 oracle 项里有 promotion 键」
// 判定，而不是看 `*string` 是否为 nil（null 与字段缺失在 Go 上都解析为 nil）。
func TestPromotionParity(t *testing.T) {
	oracle := loadClaimOracle(t)
	cases := loadClaimCases(t)
	raw := loadRawOracleKeys(t)

	for name, entry := range oracle {
		spec, ok := cases[name]
		if !ok || spec.Claim == nil {
			continue
		}
		// 只断言 oracle 里**真的带了** promotion 键的用例
		if !raw[name]["promotion"] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			got := EvaluatePromotion(*spec.Claim, T0)

			if entry.Promotion == nil {
				// oracle 说 null → 不该晋升
				if got != nil {
					t.Errorf("oracle 为 null（不晋升），但 Go 给了 %q", *got)
				}
				return
			}
			if got == nil {
				t.Errorf("want %q got nil", *entry.Promotion)
				return
			}
			if string(*got) != *entry.Promotion {
				t.Errorf("want %q got %q", *entry.Promotion, *got)
			}
		})
	}
}

// loadRawOracleKeys 返回 oracle 每项的**键集合**。
//
// **为什么需要**：JSON 里 `"promotion": null` 与字段缺失在 Go 的 `*string`
// 上都解析为 nil，但语义不同——前者是「该用例断言不晋升」，后者是「不涉及
// 晋升」。只有看原始 JSON 的键才能区分。
func loadRawOracleKeys(t *testing.T) map[string]map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claims", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败：%v", err)
	}
	var generic map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	out := make(map[string]map[string]bool, len(generic))
	for name, fields := range generic {
		keys := make(map[string]bool, len(fields))
		for k := range fields {
			keys[k] = true
		}
		out[name] = keys
	}
	return out
}

// TestPromotionNilCases —— 显式列出的「不晋升」用例。
//
// 保留此测试作为**人类可读的意图清单**（哪些用例该不晋升）。
func TestPromotionNilCases(t *testing.T) {
	cases := loadClaimCases(t)

	for _, name := range []string{
		"promotion_active_insufficient",
		"promotion_active_dup_consumers",
		"promotion_durable_candidate_too_young",
		"promotion_with_counterevidence",
		// 阈值边界：7 分钟与 9分59秒 < 10 分钟 → 不晋升
		"promotion_durable_candidate_7min",
		"promotion_durable_candidate_9min59s",
	} {
		t.Run(name, func(t *testing.T) {
			spec := cases[name]
			if spec.Claim == nil {
				t.Fatalf("%s 无 claim", name)
			}
			if got := EvaluatePromotion(*spec.Claim, T0); got != nil {
				t.Errorf("应不晋升，得到 %q", *got)
			}
		})
	}
}

// TestPromotionThresholdBoundary —— **阈值边界**（10 分钟）。
//
// 这三个用例年龄分别为 7 分钟 / 9分59秒 / 10 分钟整——用于判别阈值是否为
// 10 分钟（首版用例只有 0 与 20 分钟，都在阈值同侧，阈值变异红 0）。
func TestPromotionThresholdBoundary(t *testing.T) {
	cases := loadClaimCases(t)

	// 7 分钟 → 不够
	if got := EvaluatePromotion(*cases["promotion_durable_candidate_7min"].Claim, T0); got != nil {
		t.Errorf("7 分钟 < 10 分钟阈值，应不晋升，得到 %q", *got)
	}
	// 9分59秒 → 不够（边界内侧）
	if got := EvaluatePromotion(*cases["promotion_durable_candidate_9min59s"].Claim, T0); got != nil {
		t.Errorf("9分59秒 < 10 分钟阈值，应不晋升，得到 %q", *got)
	}
	// 10 分钟整 → 够（边界上，TS 用 `<` 故等于阈值时晋升）
	got := EvaluatePromotion(*cases["promotion_durable_candidate_10min"].Claim, T0)
	if got == nil || string(*got) != "durable" {
		t.Errorf("10 分钟整应晋升 durable，得到 %v", got)
	}
}

// TestClaimHasFileEvidenceParity —— 文件证据判定对账。
func TestClaimHasFileEvidenceParity(t *testing.T) {
	oracle := loadClaimOracle(t)
	cases := loadClaimCases(t)

	o := oracle["has_file_evidence"]
	c := cases["has_file_evidence"]

	if len(o.Checks) != len(c.Checks) {
		t.Fatalf("检查数不符：oracle=%d cases=%d", len(o.Checks), len(c.Checks))
	}
	for i, want := range o.Checks {
		got := ClaimHasFileEvidence(c.Checks[i].Claim, c.Checks[i].Path)
		if got != want.Result {
			t.Errorf("[%d] kind=%s path=%q：want %v got %v",
				i, c.Checks[i].Claim.Kind, c.Checks[i].Path, want.Result, got)
		}
	}
}

// TestStatusCountsParity —— 状态计数对账。
func TestStatusCountsParity(t *testing.T) {
	oracle := loadClaimOracle(t)
	cases := loadClaimCases(t)

	o := oracle["status_counts"]
	c := cases["status_counts"]

	got := CountClaimsByStatus(c.Claims)
	want := o.StatusCounts
	if want == nil {
		t.Fatal("oracle 缺 statusCounts")
	}
	if got.Active != want.Active {
		t.Errorf("active：want %d got %d", want.Active, got.Active)
	}
	if got.Stale != want.Stale {
		t.Errorf("stale：want %d got %d", want.Stale, got.Stale)
	}
	if got.Conflicted != want.Conflicted {
		t.Errorf("conflicted：want %d got %d", want.Conflicted, got.Conflicted)
	}
	if got.Durable != want.Durable {
		t.Errorf("durable：want %d got %d", want.Durable, got.Durable)
	}
	if got.DurableCandidate != want.DurableCandidate {
		t.Errorf("durableCandidate：want %d got %d", want.DurableCandidate, got.DurableCandidate)
	}
	if got.Quarantined != want.Quarantined {
		t.Errorf("quarantined：want %d got %d", want.Quarantined, got.Quarantined)
	}
}

// TestProposalFromAnchorParity —— anchor 派生提案对账。
func TestProposalFromAnchorParity(t *testing.T) {
	oracle := loadClaimOracle(t)
	cases := loadClaimCases(t)

	o := oracle["proposal_from_anchor"]
	c := cases["proposal_from_anchor"]

	if len(o.Anchors) != len(c.Anchors) {
		t.Fatalf("anchor 数不符：oracle=%d cases=%d", len(o.Anchors), len(c.Anchors))
	}

	meta := ClaimProposalMeta{Actor: "user", SessionID: "s1", Turn: 1, EventID: "e1", CreatedAt: T0}
	for i, want := range o.Anchors {
		got := ClaimProposalFromAnchor(c.Anchors[i].Anchor, meta)

		if string(got.Kind) != string(want.Proposal.Kind) {
			t.Errorf("[%d] kind=%s：want %q got %q", i, c.Anchors[i].Anchor.Kind, want.Proposal.Kind, got.Kind)
		}
		if string(got.Scope) != string(want.Proposal.Scope) {
			t.Errorf("[%d] scope：want %q got %q", i, want.Proposal.Scope, got.Scope)
		}
		if got.Text != want.Proposal.Text {
			t.Errorf("[%d] text：want %q got %q", i, want.Proposal.Text, got.Text)
		}
		if got.Confidence != want.Proposal.Confidence {
			t.Errorf("[%d] confidence：want %v got %v", i, want.Proposal.Confidence, got.Confidence)
		}
		if got.Fitness != want.Proposal.Fitness {
			t.Errorf("[%d] fitness：want %v got %v", i, want.Proposal.Fitness, got.Fitness)
		}
		if len(got.Evidence) != len(want.Proposal.Evidence) {
			t.Fatalf("[%d] evidence 数：want %d got %d", i, len(want.Proposal.Evidence), len(got.Evidence))
		}
		if len(got.Evidence) > 0 {
			if got.Evidence[0].ID != want.Proposal.Evidence[0].ID {
				t.Errorf("[%d] evidence ID：want %q got %q", i, want.Proposal.Evidence[0].ID, got.Evidence[0].ID)
			}
			if got.Evidence[0].Kind != want.Proposal.Evidence[0].Kind {
				t.Errorf("[%d] evidence kind：want %q got %q", i, want.Proposal.Evidence[0].Kind, got.Evidence[0].Kind)
			}
		}
		if len(got.Tags) != len(want.Proposal.Tags) {
			t.Errorf("[%d] tags 数：want %d got %d", i, len(want.Proposal.Tags), len(got.Tags))
		} else {
			for j := range got.Tags {
				if got.Tags[j] != want.Proposal.Tags[j] {
					t.Errorf("[%d] tags[%d]：want %q got %q", i, j, want.Proposal.Tags[j], got.Tags[j])
				}
			}
		}
	}
}

// TestCanRecallClaim —— 召回门禁。
func TestCanRecallClaim(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "exists.ts"), []byte("x"), 0o644)

	claimWith := func(path string) ContextClaim {
		return ContextClaim{
			Kind:     ClaimFileObservation,
			Evidence: []EvidenceRef{{ID: "e", Path: path}},
		}
	}

	// 无 cwd → 跳过检查
	if !CanRecallClaim(claimWith("whatever.ts"), "") {
		t.Error("无 cwd 应跳过检查返回 true")
	}
	// 文件存在 → 可召回
	if !CanRecallClaim(claimWith("exists.ts"), dir) {
		t.Error("文件存在应可召回")
	}
	// 文件不存在 → 阻断
	if CanRecallClaim(claimWith("missing.ts"), dir) {
		t.Error("文件不存在应阻断晋升")
	}
	// 无文件证据 → 可召回
	if !CanRecallClaim(ContextClaim{Kind: ClaimDecision}, dir) {
		t.Error("无文件证据应可召回")
	}
	// 多证据只要一个存在
	multi := ContextClaim{
		Kind: ClaimFileObservation,
		Evidence: []EvidenceRef{
			{ID: "e1", Path: "missing.ts"},
			{ID: "e2", Path: "exists.ts"},
		},
	}
	if !CanRecallClaim(multi, dir) {
		t.Error("至少一个证据存在即可召回")
	}
}
