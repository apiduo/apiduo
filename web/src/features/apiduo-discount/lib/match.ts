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
import type { DiscountRule } from '../types'

/** 拆分逗号分隔列表，去空白、去重、保序。 */
export function splitList(value: string): string[] {
  const seen = new Set<string>()
  const parts: string[] = []
  for (const raw of value.split(',')) {
    const part = raw.trim()
    if (!part || seen.has(part)) continue
    seen.add(part)
    parts.push(part)
  }
  return parts
}

/** 判断列表里是否有项命中目标；"*" 命中任意非空目标。 */
export function matchesList(list: string, target: string): boolean {
  if (!target) return false
  return splitList(list).some((part) => part === '*' || part === target)
}

export function isExpired(rule: DiscountRule, nowSeconds: number): boolean {
  return rule.until > 0 && nowSeconds >= rule.until
}

export function isValidRate(rate: number): boolean {
  return Number.isFinite(rate) && rate > 0 && rate < 1
}

/**
 * 按数组顺序取第一条命中的规则。
 *
 * 与后端 discount.Resolve 保持一致：只跳过过期和非法倍率，
 * 不因为「分组不可用」而跳过——那是配置错误，靠警告提示，不改变计费结果。
 */
export function resolveDiscount(
  rules: DiscountRule[],
  group: string,
  model: string,
  nowSeconds: number
): { index: number; rate: number } | null {
  for (let index = 0; index < rules.length; index += 1) {
    const rule = rules[index]
    if (isExpired(rule, nowSeconds) || !isValidRate(rule.rate)) continue
    if (!matchesList(rule.groups, group) || !matchesList(rule.models, model)) {
      continue
    }
    return { index, rate: rule.rate }
  }
  return null
}

/**
 * 一个分组下「生效价格」的分段。
 *
 * 生效价格是 (分组, 模型) 上的分段常函数：值只在规则边界上变化，所以段数
 * 上限是规则数 + 1，和模型总量无关。预览必须按段渲染，不能按模型枚举——
 * 枚举只能用少量探针代表全集，模型上千时必然抽错样本，还会把「其余模型」
 * 当成一个价格，而它其实是个异质集合。
 */
export type EffectiveSegment =
  | {
      kind: 'rule'
      /** 规则下标，用于和上方行号对应 */
      index: number
      rate: number
      /** 这条规则在该分组下真正接管的模型名（已扣除前面接管的部分） */
      models: string[]
      /** true 表示接管此前未被点名的全部模型，且后面不可能再有段 */
      catchesRest: boolean
      until: number
    }
  | {
      kind: 'base'
      rate: number
      /** 候选模型里没被任何规则接管的数量；候选未知时为 0 */
      restCount: number
    }

/**
 * 把一个分组的生效价格拆成有序分段，语义与 resolveDiscount 完全一致。
 *
 * 逐条规则收集它「首次接管」的模型；遇到 models 含 "*" 的规则，剩余全部模型
 * 都归它，直接收尾——这正是「命中即停」在模型维度上的含义。
 */
export function summarizeGroup(
  rules: DiscountRule[],
  group: string,
  candidates: string[],
  baseRate: number,
  nowSeconds: number
): EffectiveSegment[] {
  const claimed = new Set<string>()
  const segments: EffectiveSegment[] = []

  for (let index = 0; index < rules.length; index += 1) {
    const rule = rules[index]
    if (isExpired(rule, nowSeconds) || !isValidRate(rule.rate)) continue
    if (!matchesList(rule.groups, group)) continue

    const models = splitList(rule.models)
    if (models.includes('*')) {
      segments.push({
        kind: 'rule',
        index,
        rate: rule.rate,
        models: [],
        catchesRest: true,
        until: rule.until,
      })
      return segments
    }

    const fresh = models.filter((model) => !claimed.has(model))
    if (fresh.length === 0) continue
    for (const model of fresh) claimed.add(model)
    segments.push({
      kind: 'rule',
      index,
      rate: rule.rate,
      models: fresh,
      catchesRest: false,
      until: rule.until,
    })
  }

  segments.push({
    kind: 'base',
    rate: baseRate,
    restCount: candidates.filter((model) => !claimed.has(model)).length,
  })
  return segments
}

/**
 * 找出永远不会命中的行。
 *
 * 一行的价值在于「有至少一个组×模型组合，第一次命中就是它」。判断时不解模型目录：
 * 点名了什么就只检查那些名字，写 "*" 就视为覆盖无限模型空间（新模型随时上线）。
 * 所以 1000 个模型和 3 个模型的成本一样，也不会因为抽样漏掉第 5 个点名模型。
 */
export function findDeadRows(
  rules: DiscountRule[],
  groups: string[],
  nowSeconds: number
): boolean[] {
  const parsed = rules.map((rule) => {
    const ruleGroups = splitList(rule.groups)
    const ruleModels = splitList(rule.models)
    return {
      groups: ruleGroups,
      groupSet: new Set(ruleGroups),
      models: ruleModels,
      modelSet: new Set(ruleModels),
      groupAll: ruleGroups.includes('*'),
      modelAll: ruleModels.includes('*'),
      live: !isExpired(rule, nowSeconds) && isValidRate(rule.rate),
    }
  })

  return parsed.map((current, index) => {
    if (!current.live) return false

    const targetGroups = current.groupAll
      ? groups
      : current.groups.filter((group) => groups.includes(group))
    if (targetGroups.length === 0) return false
    if (!current.modelAll && current.models.length === 0) return false

    for (const group of targetGroups) {
      if (current.modelAll) {
        // 模型空间无限，只有更早的 "*" 规则能整体盖住它
        let covered = false
        for (let earlier = 0; earlier < index; earlier += 1) {
          const other = parsed[earlier]
          if (!other.live || !other.modelAll) continue
          if (other.groupAll || other.groupSet.has(group)) {
            covered = true
            break
          }
        }
        if (!covered) return false
        continue
      }

      for (const model of current.models) {
        let covered = false
        for (let earlier = 0; earlier < index; earlier += 1) {
          const other = parsed[earlier]
          if (!other.live) continue
          if (!other.groupAll && !other.groupSet.has(group)) continue
          if (other.modelAll || other.modelSet.has(model)) {
            covered = true
            break
          }
        }
        if (!covered) return false
      }
    }
    return true
  })
}

export type RuleDiagnosis = {
  /** 会阻断保存，和服务端的校验一一对应 */
  errors: { groups?: string; models?: string; rate?: string }
  /** 能保存但不会按预期生效，属于「配了也没用」 */
  warnings: string[]
}

export type DiagnoseContext = {
  knownGroups: string[]
  usableGroups: string[]
  /** 目录里的模型名，用于发现拼错；集合由调用方缓存，避免每条规则重建 */
  knownModels: Set<string>
  /** 分组倍率，规则是覆盖而非相乘，高于它就是涨价 */
  groupRatios: Record<string, number>
  nowSeconds: number
  /** 已被前面的行完全盖住时，涨价提示没有意义 */
  dead?: boolean
  /** i18n 文案由调用方注入，纯逻辑只负责判定 */
  messages: {
    groupsRequired: string
    modelsRequired: string
    rateRange: string
    groupUnknown: (group: string) => string
    groupUnusable: (group: string) => string
    modelUnknown: (models: string[]) => string
    priceIncrease: (group: string, baseRatio: number, rate: number) => string
    globalScope: string
  }
}

export function diagnoseRule(
  rule: DiscountRule,
  context: DiagnoseContext
): RuleDiagnosis {
  const errors: RuleDiagnosis['errors'] = {}
  const warnings: string[] = []

  const groups = splitList(rule.groups)
  const models = splitList(rule.models)

  if (groups.length === 0) errors.groups = context.messages.groupsRequired
  if (models.length === 0) errors.models = context.messages.modelsRequired
  if (!isValidRate(rule.rate)) errors.rate = context.messages.rateRange

  for (const group of groups) {
    if (group === '*') continue
    if (!context.knownGroups.includes(group)) {
      warnings.push(context.messages.groupUnknown(group))
      continue
    }
    if (
      context.usableGroups.length > 0 &&
      !context.usableGroups.includes(group)
    ) {
      warnings.push(context.messages.groupUnusable(group))
    }
  }

  // 下面都是「会真的影响账单」的提示，过期或被盖住的行不必再提
  if (isExpired(rule, context.nowSeconds) || context.dead) {
    return { errors, warnings }
  }

  if (context.knownModels.size > 0) {
    const unknown = models.filter(
      (model) => model !== '*' && !context.knownModels.has(model)
    )
    if (unknown.length > 0) {
      warnings.push(context.messages.modelUnknown(unknown))
    }
  }

  if (groups.includes('*') && models.includes('*')) {
    warnings.push(context.messages.globalScope)
  }

  // 命中即覆盖组价：rate 高于组价就是涨价，不是折扣
  if (isValidRate(rule.rate)) {
    const wildcardGroup = groups.includes('*')
    const fallbackGroups =
      context.usableGroups.length > 0
        ? context.usableGroups
        : context.knownGroups
    const covered = wildcardGroup
      ? fallbackGroups
      : groups.filter(
          (group) =>
            context.knownGroups.includes(group) &&
            (context.usableGroups.length === 0 ||
              context.usableGroups.includes(group))
        )

    for (const group of covered) {
      const baseRatio = context.groupRatios[group]
      if (!baseRatio || baseRatio <= 0) continue
      if (rule.rate / baseRatio > 1.005) {
        warnings.push(
          context.messages.priceIncrease(group, baseRatio, rule.rate)
        )
      }
    }
  }

  return { errors, warnings }
}

export function hasBlockingError(diagnosis: RuleDiagnosis): boolean {
  return Object.keys(diagnosis.errors).length > 0
}

/** 把 0.8 显示成 "8"，0.75 显示成 "7.5"。 */
export function rateToZhe(rate: number): string {
  return String(Math.round(rate * 100) / 10)
}

/** 把 "8" 转成 0.8；非法输入返回 NaN。 */
export function zheToRate(zhe: string): number {
  const parsed = Number.parseFloat(zhe)
  if (!Number.isFinite(parsed)) return Number.NaN
  return Math.round(parsed * 10) / 100
}

/** unix 秒 → <input type="date"> 的值（本地时区）。 */
export function untilToDateValue(until: number): string {
  if (!until) return ''
  const date = new Date(until * 1000)
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${date.getFullYear()}-${month}-${day}`
}

/**
 * <input type="date"> 的值 → 该日 23:59:59 的 unix 秒。
 *
 * 「到期 12/31」必须是 12/31 全天有效，不是当天零点失效；按本地时区解释，
 * 避免录入端与服务端时区不一致导致差 8 小时。
 */
export function dateValueToUntil(value: string): number {
  if (!value) return 0
  const [year, month, day] = value.split('-').map(Number)
  if (!year || !month || !day) return 0
  return Math.floor(new Date(year, month - 1, day, 23, 59, 59).getTime() / 1000)
}
