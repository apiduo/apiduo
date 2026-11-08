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

/**
 * 代表「任何未在规则里点名的模型」的哨兵。
 *
 * 死行检测和预览都需要一个不属于任何具体规则的名字：否则一条 "*" 规则会因为
 * 探针全是它自己覆盖的模型而永远看不出是否被前面的行截断。
 * 用不可见字符开头，保证不会和真实模型名撞车。
 */
export const OTHER_MODEL = '\u0000other'

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
 * 收集预览与死行检测用的探针模型。
 *
 * 规则里点名的具体模型 + 一个「其他模型」哨兵。只点名 "*" 时只剩哨兵，
 * 预览就是每组一个价，符合实际。
 */
export function collectProbeModels(
  rules: DiscountRule[],
  fallbackModels: string[],
  limit = 4
): string[] {
  const named: string[] = []
  for (const rule of rules) {
    for (const model of splitList(rule.models)) {
      if (model === '*' || named.includes(model)) continue
      named.push(model)
    }
  }

  const probes = named.length > 0 ? named : fallbackModels.slice(0, limit)
  return [...probes.slice(0, limit), OTHER_MODEL]
}

/**
 * 找出永远不会命中的行。
 *
 * 一行的价值在于「有至少一个组×模型组合，第一次命中就是它」。逐个枚举它自己
 * 覆盖的组合，只要有一个组合轮到它，就不是死行。前面的行把它完全盖住时，
 * 它一条都不剩——这正是把兜底行拖到最上面后其余全部失效的原因。
 */
export function findDeadRows(
  rules: DiscountRule[],
  groups: string[],
  probes: string[],
  nowSeconds: number
): boolean[] {
  return rules.map((rule, index) => {
    if (isExpired(rule, nowSeconds) || !isValidRate(rule.rate)) return false

    const ruleGroups = splitList(rule.groups)
    const ruleModels = splitList(rule.models)
    if (ruleGroups.length === 0 || ruleModels.length === 0) return false

    const coveredGroups =
      ruleGroups.includes('*') || ruleGroups.length === 0
        ? groups
        : ruleGroups.filter((group) => groups.includes(group))

    const coveredModels = ruleModels.includes('*')
      ? probes
      : ruleModels.filter((model) => probes.includes(model))

    if (coveredGroups.length === 0 || coveredModels.length === 0) return false

    for (const group of coveredGroups) {
      for (const model of coveredModels) {
        const hit = resolveDiscount(rules, group, model, nowSeconds)
        if (hit?.index === index) return false
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
  /** i18n 文案由调用方注入，纯逻辑只负责判定 */
  messages: {
    groupsRequired: string
    modelsRequired: string
    rateRange: string
    groupUnknown: (group: string) => string
    groupUnusable: (group: string) => string
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
