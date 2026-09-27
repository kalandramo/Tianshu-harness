/**
 * provider-presets-volc — 火山方舟预设数据（按量 `volc` / Agent Plan 双协议）。
 *
 * 独立成文件：provider-presets.ts 是行数棘轮点名的巨石（ceiling 只降不升），
 * 大块纯数据沿「同一 provider 家族」接缝拆出；PROVIDER_PRESETS 的合并与
 * key→预设 的类型索引仍留在 provider-presets.ts。
 */

import type { ProviderPreset } from './provider-presets.js'
import type { ModelConfig } from './schema.js'

/**
 * 火山方舟 Agent Plan 个人版（订阅制）文本生成 fleet —— OpenAI 协议
 * （/api/plan/v3）与 Anthropic 协议（/api/plan）两个预设端点共用。
 *
 * 官方文档（docs.volcengine.com/docs/ark/agent-plan-personal-*，2026-09 口径）：
 *   · OpenAI 兼容面：https://ark.cn-beijing.volces.com/api/plan/v3
 *     （chat/completions 与 responses 共用；Codex 走 wire_api=responses）
 *   · Anthropic Messages：https://ark.cn-beijing.volces.com/api/plan
 *   · 专属 API Key 与按量「方舟 API Key」/ Coding Plan Key 互不通用（计费
 *     红线：混用会按量另计费或 401）。
 *   · 端点没有 GET /models（模型目录走控制台/控制面 OpenAPI）——连接测试会
 *     跳过列表走最小补全，见 api/endpoint-map.ts 的 hasModelsListEndpoint。
 *   · 计费走套餐 AFP 额度（非按 token）：单价清零 + cost-model 记为订阅制。
 *
 * 型号与规格取自官方 OpenCode 接入示例（Chat API / Responses API 两版一致：
 * ark-code-latest 256K/32K，其余 1M/64K；含输入模态声明）。
 */

/**
 * Agent Plan 上未列入方舟「调节思考长度」支持表（deep-thinking 文档）的
 * 第三方模型（Kimi / MiniMax）：模型级关掉档位通道，避免 reasoning_effort
 * 打到不支持的模型上吃 400。Kimi/minimax 自营 API 的档位语义与方舟网关不同，
 * 待真机核实支持后逐个摘掉本标记（kimi-k2.7-code 另有 Codex 文档明示不支持
 * reasoning 的双重证据）。
 */
const AGENT_PLAN_NO_EFFORT_CAPABILITIES: NonNullable<ModelConfig['capabilities']> = { effortFormat: 'none' }

function volcPlanModels(): ModelConfig[] {
  return [
    {
      id: 'ark-code-latest',
      description: '控制台当前选择的模型（含 Auto 路由）——换模型无需改配置',
      contextWindow: 256_000,
      maxTokens: 32_000,
      tier: 'strong',
      supportsVision: true,
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    },
    {
      id: 'doubao-seed-2.1-pro',
      description: '豆包进阶旗舰：1M 上下文 + 图片输入',
      contextWindow: 1_024_000,
      maxTokens: 65_536,
      tier: 'strong',
      supportsVision: true,
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    },
    {
      id: 'doubao-seed-evolving',
      description: '豆包持续迭代档：1M 上下文 + 图片输入',
      contextWindow: 1_024_000,
      maxTokens: 65_536,
      tier: 'strong',
      supportsVision: true,
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    },
    {
      id: 'doubao-seed-2.1-lite',
      description: '豆包标准轻量档：1M 上下文 + 图片输入',
      contextWindow: 1_024_000,
      maxTokens: 65_536,
      tier: 'cheap',
      supportsVision: true,
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    },
    {
      id: 'doubao-seed-2.0-mini',
      description: '豆包极速档：256K 上下文 + 图片输入',
      contextWindow: 256_000,
      maxTokens: 65_536,
      tier: 'cheap',
      supportsVision: true,
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    },
    {
      id: 'glm-5.3',
      description: 'GLM 文本旗舰：1M 上下文（默认开启思考，不可关闭）',
      contextWindow: 1_024_000,
      maxTokens: 65_536,
      tier: 'strong',
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    },
    {
      id: 'glm-5.3-flash',
      description: 'GLM 原生多模态快速档：1M 上下文 + 图片输入',
      contextWindow: 1_024_000,
      maxTokens: 65_536,
      tier: 'strong',
      supportsVision: true,
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    },
    {
      id: 'deepseek-v4-flash',
      description: 'DeepSeek 标准档：1M 上下文，长会话性价比',
      contextWindow: 1_024_000,
      maxTokens: 65_536,
      tier: 'cheap',
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    },
    {
      id: 'deepseek-v4-pro',
      description: 'DeepSeek 旗舰推理档：1M 上下文',
      contextWindow: 1_024_000,
      maxTokens: 65_536,
      tier: 'strong',
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    },
    {
      id: 'deepseek-v4.1-flash',
      description: 'DeepSeek V4.1 线：1M 上下文 + 原生多模态',
      contextWindow: 1_024_000,
      maxTokens: 65_536,
      tier: 'strong',
      supportsVision: true,
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    },
    {
      id: 'minimax-m3',
      description: 'MiniMax 旗舰：1M 上下文 + 图片输入',
      contextWindow: 1_024_000,
      maxTokens: 65_536,
      tier: 'strong',
      supportsVision: true,
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      capabilities: AGENT_PLAN_NO_EFFORT_CAPABILITIES,
    },
    {
      id: 'kimi-k2.7-code',
      description: 'Kimi 代码档：256K 上下文',
      contextWindow: 256_000,
      maxTokens: 32_000,
      tier: 'strong',
      supportsVision: true,
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      capabilities: AGENT_PLAN_NO_EFFORT_CAPABILITIES,
    },
    {
      id: 'kimi-k3',
      description: 'Kimi 旗舰：1M 上下文 + 图片输入（默认开启思考）',
      contextWindow: 1_024_000,
      maxTokens: 65_536,
      tier: 'strong',
      supportsVision: true,
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      capabilities: AGENT_PLAN_NO_EFFORT_CAPABILITIES,
    },
    {
      id: 'kimi-k2.8-preview',
      description: 'Kimi 预览档：1M 上下文 + 图片输入',
      contextWindow: 1_024_000,
      maxTokens: 65_536,
      tier: 'strong',
      supportsVision: true,
      pricing: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      capabilities: AGENT_PLAN_NO_EFFORT_CAPABILITIES,
    },
  ]
}

export const VOLC_PRESETS: Record<'volc' | 'volc-plan' | 'volc-plan-anthropic', ProviderPreset> = {
  volc: {
    key: 'volc',
    label: '火山方舟 (豆包)',
    description: '火山引擎方舟：豆包 Doubao 系列，OpenAI 兼容端点',
    defaultModelId: 'doubao-seed-2.0-pro',
    keyUrl: 'https://console.volcengine.com/ark',
    provider: {
      name: 'volc',
      apiKeyEnv: 'VOLC_API_KEY',
      baseUrl: 'https://ark.cn-beijing.volces.com/api/v3',
      protocol: 'openai',
      capabilities: {
        cacheControl: false,
        stripParams: [],
        toolJsonBug: false,
        prefixCache: 'none',
        prefixCompletion: false,
        // 档位通道声明在下面的 doubao-seed-2.0-pro **模型级**（见该条注释）：
        // 方舟按量端点能拉到 embedding/vision/语音等异构型号，provider 级声明
        // 会把 reasoning_effort 发给不支持 reasoning 的型号。
      },
      thinking: 'enabled',
      maxTokens: 32_768,
      // 方舟模型以控制台接入点为准——探测（/v3/models）能拉到真实列表，
      // 下表仅兜底推荐，型号随方舟发布更新。
      models: [
        {
          id: 'doubao-seed-2.0-pro',
          description: '豆包旗舰（以方舟控制台接入点为准）',
          contextWindow: 262_144,
          maxTokens: 32_768,
          reasoningEffort: 'high',
          tier: 'strong',
          // 方舟 Chat API 官方档位通道（deep-thinking 文档）：reasoning_effort
          // none|minimal|low|medium|high|xhigh|max——支持该字段的模型全部接受 7 档、
          // 越界档由服务端按模型映射。off→none（官方"关闭思考"枚举；内部 off 永不
          // 直发）。doubao-seed-2.0-pro 对应支持表内的 doubao-seed-2-0-pro-260215。
          capabilities: {
            thinkingBlock: 'none',
            effortFormat: 'reasoning_effort',
            effortCap: { off: 'none' },
          },
        },
        {
          id: 'doubao-seed-2.0-flash',
          description: '豆包快速档：低延迟轻量任务',
          contextWindow: 131_072,
          maxTokens: 16_384,
          reasoningEffort: 'medium',
          tier: 'cheap',
          // 该 id 不在方舟当前模型列表与深度思考支持表内（方舟型号以控制台接入点
          // 为准）——不开档位通道，避免向未验证端点发 reasoning_effort。
        },
      ],
      unsupported: [],
    },
  },

  // 火山方舟 Agent Plan 个人版（订阅制，issue #272 第一方接入）——
  // OpenAI 协议端点（chat/completions + responses）。fleet/计费/无 /models 的
  // 口径见 volcPlanModels() 上方注释。
  'volc-plan': {
    key: 'volc-plan',
    label: '火山方舟 Agent Plan',
    description: '火山方舟订阅制 Agent Plan：专属 /api/plan/v3 端点，1M 上下文多模型，走套餐 AFP 额度',
    defaultModelId: 'ark-code-latest',
    keyUrl: 'https://console.volcengine.com/ark/region:ark+cn-beijing/openManagement?LLM=%7B%7D&OpenModelVisible=false&advancedActiveKey=agentPlan',
    provider: {
      name: 'volc-plan',
      apiKeyEnv: 'ARK_PLAN_API_KEY',
      baseUrl: 'https://ark.cn-beijing.volces.com/api/plan/v3',
      protocol: 'openai',
      capabilities: {
        cacheControl: false,
        stripParams: [],
        toolJsonBug: false,
        prefixCache: 'none',
        prefixCompletion: false,
        // 方舟档位通道（官方 deep-thinking 口径）：reasoning_effort 的 7 档全部
        // 接受、越界档由服务端按模型映射；off→none。provider 级声明 + 模型级
        // opt-out（volcPlanModels 的 AGENT_PLAN_NO_EFFORT_CAPABILITIES）：套餐
        // fleet 是策划过的文本生成型号，未列入方舟支持表的 kimi/minimax 已逐个
        // 关闭；用户自加的型号默认继承该通道（Agent Plan 场景基本是文本推理型号）。
        thinkingBlock: 'none',
        effortFormat: 'reasoning_effort',
        effortCap: { off: 'none' },
      },
      thinking: 'enabled',
      maxTokens: 32_768,
      models: volcPlanModels(),
      unsupported: [],
    },
  },
  // 同一订阅的 Anthropic Messages 端点（Claude Code 官方接入地址）——
  // 鉴权头/请求体走 anthropic 客户端，模型 fleet 与鉴权与 volc-plan 同源。
  'volc-plan-anthropic': {
    key: 'volc-plan-anthropic',
    label: '火山方舟 Agent Plan (Anthropic)',
    description: '火山方舟订阅制 Agent Plan 的 /api/plan Messages 端点（Claude Code 官方接入地址）',
    defaultModelId: 'ark-code-latest',
    keyUrl: 'https://console.volcengine.com/ark/region:ark+cn-beijing/openManagement?LLM=%7B%7D&OpenModelVisible=false&advancedActiveKey=agentPlan',
    provider: {
      name: 'volc-plan-anthropic',
      apiKeyEnv: 'ARK_PLAN_API_KEY',
      baseUrl: 'https://ark.cn-beijing.volces.com/api/plan',
      protocol: 'anthropic',
      capabilities: {
        // Messages API 官方文档的 output_config.effort（none/minimal/low/medium/
        // high/xhigh/max）——anthropic 客户端已实现该通道：不再发 budget_tokens
        // 形态的 thinking 块，时间预算仍按思考请求计（90s/180s）。未列入方舟
        // 支持表的 kimi/minimax 型号由 fleet 模型级 opt-out 关闭。
        // cache_control 断点官方文档未见；usage.cache_creation_input_tokens 恒 0
        // （"当前暂不支持该计费方式"）→ 与 OpenAI 面同款保守声明。
        cacheControl: false,
        stripParams: [],
        toolJsonBug: false,
        prefixCache: 'none',
        prefixCompletion: false,
        effortFormat: 'output_config',
        effortCap: { off: 'none' },
      },
      thinking: 'enabled',
      maxTokens: 32_768,
      models: volcPlanModels(),
      unsupported: [],
    },
  },
}
