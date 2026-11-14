/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { getFreshAuthHeaders } from "@/lib/api";

import type { McpKind } from "./types";

/**
 * 一键补全：拿模型名与已知事实，让一个大模型把「输入限制 / 参数约束 / 能力描述」
 * 填出来。
 *
 * 三条硬约束，宁可少填也不能违反：
 *
 * 1. **不确定就留空。** 这段描述会进 tools/list 给 agent 读，写错等于教 agent
 *    按错误能力调用。所以 prompt 里明确要求 null，解析时也会把越界/类型不符的
 *    值直接丢掉，而不是「修一修接着用」。
 * 2. **0 与 null 语义不同。** null = 未声明（不拦截），0 = 明确不支持。
 *    模型说「不知道」时必须是 null，写成 0 会把图生图模型锁死。
 * 3. **永远有产出。** 模型不可用（没配分组、渠道挂了、超时、返回不是 JSON）
 *    时退回本地规则，至少把 summary/tags 填上，其余保持未声明。
 */

/** 送进去的已知事实，全部来自本地，不含密钥。 */
export type AssistFacts = {
  modelId: string;
  kind: McpKind;
  async: boolean;
  displayName: string;
  /** 渠道类型显示名，例如 "Doubao"；没有就省略。 */
  channelType?: string;
  /** 上游域名（只到 host），用来区分同名不同厂。 */
  upstreamHost?: string;
  /** 编辑已有声明时把现值带上，避免模型重复推荐同一份内容。 */
  current?: {
    summary: string;
    tags: string;
    parameters: string;
  };
};

/** 只带「有意义」的字段：undefined 表示模型没给或给了不合法的值。 */
export type AssistPatch = {
  display_name?: string;
  summary?: string;
  tags?: string;
  async?: boolean;
  max_ref_images?: number | null;
  max_ref_videos?: number | null;
  max_prompt_chars?: number | null;
  parameters?: string;
};

export type AssistOutcome = {
  patch: AssistPatch;
  confidence: "high" | "medium" | "low";
  notes: string;
  /** ai = 模型返回；rule = 模型不可用时的本地兜底。 */
  source: "ai" | "rule";
};

/**
 * 标签候选。给模型一个封闭集合能显著降低胡编概率，但不禁自定义 ——
 * 新模型总有新卖点，写死会逼着改代码。
 */
export const TAG_HINTS = [
  "文生图",
  "图生图",
  "图生视频",
  "首尾帧",
  "多参考图",
  "异步任务",
  "同步返回",
  "中文提示词",
  "快速生成",
  "高清",
  "多图输出",
];

const SYSTEM_PROMPT = `你是 API 网关的「模型能力声明」助手。输入是某个模型在网关里的已知事实，请补全它的 MCP 能力声明。

硬性规则，违反任何一条都不如留空：
1. 只输出一个 JSON 对象，不要 markdown 代码块、不要解释、不要前后缀。
2. 不确定的字段一律给 null。严禁猜测具体数值（参考图上限、提示词上限、参数枚举）。
3. 0 和 null 含义不同：null = 未声明（不拦截），0 = 明确不支持。不确定时必须 null。
4. summary 用中文，不超过 40 个字，只写事实（能做什么、产物是什么），不要形容词堆砌，不要写「强大」「领先」「革命性」。
5. tags 从这些里选，也可以补同风格的短词，最多 6 个：${TAG_HINTS.join("、")}。
6. parameters 只写你确定的参数，结构为 {"参数名": {"enum": [...]}} 或 {"参数名": {"min": 1, "max": 10}}，可带 "default"。没有把握就 null。
7. async：该模型的调用是同步返回还是异步任务（要轮询 task id）。不确定给 null。

输出字段：
{"summary": string|null, "tags": string[]|null, "async": boolean|null,
 "max_ref_images": integer|null, "max_ref_videos": integer|null,
 "max_prompt_chars": integer|null, "parameters": object|null,
 "confidence": "high"|"medium"|"low", "notes": string}

confidence 表示你对自己输出的整体把握：事实来自明确文档/常识给 high，靠命名推断给 medium，大部分是猜的给 low。notes 用一句话说明依据与不确定处。`;

/** 拼给模型看的事实。字段名用中文，模型对中文语义更敏感。 */
export function buildAssistMessages(
  facts: AssistFacts,
): { role: "system" | "user"; content: string }[] {
  let kindText = "未知";
  if (facts.kind === "video") kindText = "视频";
  else if (facts.kind === "image") kindText = "图像";
  const lines = [
    `模型 ID：${facts.modelId}`,
    `产物类型：${kindText}`,
    `执行方式：${facts.async ? "异步任务" : "同步返回"}`,
  ];
  if (facts.displayName.trim()) {
    lines.push(`显示名：${facts.displayName.trim()}`);
  }
  if (facts.channelType) lines.push(`渠道类型：${facts.channelType}`);
  if (facts.upstreamHost) lines.push(`上游域名：${facts.upstreamHost}`);
  if (facts.current) {
    if (facts.current.summary.trim()) {
      lines.push(`已有的一句话描述：${facts.current.summary.trim()}`);
    }
    if (facts.current.tags.trim()) {
      lines.push(`已有的标签：${facts.current.tags.trim()}`);
    }
    if (facts.current.parameters.trim()) {
      lines.push(`已有的参数约束：${facts.current.parameters.trim()}`);
    }
  }
  lines.push("请补全这份声明。");

  return [
    { role: "system", content: SYSTEM_PROMPT },
    { role: "user", content: lines.join("\n") },
  ];
}

/** 模型爱套 ```json，先剥壳再解析。 */
function extractJSON(text: string): unknown {
  const trimmed = text.trim();
  const fenced = /^```(?:json)?\s*([\s\S]*?)\s*```$/i.exec(trimmed);
  const body = fenced ? fenced[1] : trimmed;
  try {
    return JSON.parse(body);
  } catch {
    // 有些模型会在 JSON 前后加一句话，取第一个 { 到最后一个 }。
    const start = body.indexOf("{");
    const end = body.lastIndexOf("}");
    if (start === -1 || end <= start) return null;
    try {
      return JSON.parse(body.slice(start, end + 1));
    } catch {
      return null;
    }
  }
}

function pickText(value: unknown, maxLen: number): string | undefined {
  if (typeof value !== "string") return undefined;
  const text = value.trim();
  if (!text) return undefined;
  return text.slice(0, maxLen);
}

/**
 * 三态整数：null 有意义（未声明），所以不能用 undefined 表示 null。
 * 上限按字段传：参考图/视频是「几张」，提示词上限是「多少字」，量级差三位。
 */
function pickCount(value: unknown, max: number): number | null | undefined {
  if (value === null) return null;
  if (typeof value !== "number" || !Number.isFinite(value)) return undefined;
  if (!Number.isInteger(value) || value < 0 || value > max) return undefined;
  return value;
}

function pickBool(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

function pickTags(value: unknown): string | undefined {
  if (!Array.isArray(value)) return undefined;
  const seen = new Set<string>();
  const tags: string[] = [];
  for (const item of value) {
    const tag = pickText(item, 20);
    if (!tag || seen.has(tag)) continue;
    seen.add(tag);
    tags.push(tag);
    if (tags.length >= 6) break;
  }
  return tags.length > 0 ? tags.join(",") : undefined;
}

/**
 * 只保留后端 ParamSpec 认识的四个键（enum / min / max / default）。
 * 多出来的键后端会忽略，但留在 JSON 里会让人以为生效了。
 */
function pickParameters(value: unknown): string | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return undefined;
  }
  const out: Record<string, Record<string, unknown>> = {};
  for (const [rawName, rawSpec] of Object.entries(
    value as Record<string, unknown>,
  )) {
    const name = rawName.trim().slice(0, 64);
    if (!name) continue;
    if (!rawSpec || typeof rawSpec !== "object" || Array.isArray(rawSpec)) {
      continue;
    }
    const spec = rawSpec as Record<string, unknown>;
    const kept: Record<string, unknown> = {};
    if (Array.isArray(spec.enum)) {
      const values = spec.enum
        .filter(
          (v) =>
            typeof v === "string" ||
            typeof v === "number" ||
            typeof v === "boolean",
        )
        .slice(0, 30);
      if (values.length > 0) kept.enum = values;
    }
    for (const key of ["min", "max"] as const) {
      const n = spec[key];
      if (typeof n === "number" && Number.isFinite(n)) kept[key] = n;
    }
    if (kept.min !== undefined && kept.max !== undefined) {
      if ((kept.min as number) > (kept.max as number)) delete kept.max;
    }
    // enum 之外的 default 才安全：范围里的数字无法验证是否合法。
    if (kept.enum && spec.default !== undefined) {
      if ((kept.enum as unknown[]).includes(spec.default)) {
        kept.default = spec.default;
      }
    }
    if (Object.keys(kept).length > 0) out[name] = kept;
  }
  return Object.keys(out).length > 0 ? JSON.stringify(out, null, 2) : undefined;
}

/**
 * 把模型回复解析成 patch。解析不出来返回 null —— 交给调用方降级，
 * 而不是抛异常把整次操作变成「什么都没发生」。
 */
export function parseAssistReply(text: string): AssistOutcome | null {
  const parsed = extractJSON(text);
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    return null;
  }
  const raw = parsed as Record<string, unknown>;

  const patch: AssistPatch = {};
  const summary = pickText(raw.summary, 120);
  if (summary) patch.summary = summary;
  const tags = pickTags(raw.tags);
  if (tags) patch.tags = tags;
  const asyncValue = pickBool(raw.async);
  if (asyncValue !== undefined) patch.async = asyncValue;

  const refImages = pickCount(raw.max_ref_images, 100);
  if (refImages !== undefined) patch.max_ref_images = refImages;
  const refVideos = pickCount(raw.max_ref_videos, 20);
  if (refVideos !== undefined) patch.max_ref_videos = refVideos;
  const promptChars = pickCount(raw.max_prompt_chars, 1_000_000);
  if (promptChars !== undefined) patch.max_prompt_chars = promptChars;

  const parameters = pickParameters(raw.parameters);
  if (parameters) patch.parameters = parameters;

  if (Object.keys(patch).length === 0) return null;

  const confidence =
    raw.confidence === "high" || raw.confidence === "medium"
      ? raw.confidence
      : "low";
  return {
    patch,
    confidence,
    notes: pickText(raw.notes, 200) ?? "",
    source: "ai",
  };
}

/**
 * 本地兜底：不联网也能给出「这句话 + 标签」。
 *
 * 只写从 kind/async 能直接推出来的事实，不猜任何上限。
 * 参数约束留空 —— 猜错枚举比不填更糟。
 */
export function ruleAssist(facts: AssistFacts): AssistOutcome {
  const isVideo = facts.kind === "video";
  const kindText = isVideo ? "视频" : "图像";
  const tags = new Set<string>([isVideo ? "文生视频" : "文生图"]);
  tags.add(facts.async ? "异步任务" : "同步返回");

  const label = facts.displayName.trim() || facts.modelId;
  const patch: AssistPatch = {
    summary: `${label}：${kindText}生成模型`,
    tags: [...tags].join(","),
  };
  if (facts.async) patch.async = true;

  return {
    patch,
    confidence: "low",
    notes: "按产物类型推导，未使用模型",
    source: "rule",
  };
}

const ASSIST_TIMEOUT_MS = 30_000;

/**
 * 走 /pg/chat/completions：管理端已有会话鉴权，渠道选择/计费/日志全部复用，
 * 不需要在 MCP 里再养一套 LLM 客户端。
 *
 * 任何失败（模型不在分组、渠道异常、超时、非 JSON）都退回规则补全，
 * 让「一键」永远有结果。
 */
export async function runAssist(
  facts: AssistFacts,
  model: string,
  signal?: AbortSignal,
): Promise<AssistOutcome> {
  // 没选辅助模型就直接走本地规则：发一个 model 为空的请求只会拿回 400，
  // 既没有结果，还会在控制台留一条无意义的报错。
  if (!model.trim()) return ruleAssist(facts);

  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), ASSIST_TIMEOUT_MS);
  const abort = () => controller.abort();
  signal?.addEventListener("abort", abort);

  try {
    const headers = await getFreshAuthHeaders();
    const response = await fetch("/pg/chat/completions", {
      method: "POST",
      headers: { ...headers, "Content-Type": "application/json" },
      credentials: "include",
      signal: controller.signal,
      body: JSON.stringify({
        model,
        messages: buildAssistMessages(facts),
        temperature: 0.1,
        max_tokens: 800,
        stream: false,
      }),
    });
    if (!response.ok) {
      const detail = await response.text();
      throw new Error(`HTTP ${response.status} ${detail.slice(0, 200)}`);
    }
    const body = (await response.json()) as {
      choices?: { message?: { content?: string } }[];
    };
    const content = body.choices?.[0]?.message?.content ?? "";
    const parsed = parseAssistReply(content);
    if (!parsed) throw new Error("模型没有返回可用的 JSON");
    return parsed;
  } catch {
    return ruleAssist(facts);
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener("abort", abort);
  }
}
