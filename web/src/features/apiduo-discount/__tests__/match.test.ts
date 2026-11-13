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
import { describe, expect, it } from 'vitest'

import {
  dateValueToUntil,
  diagnoseRule,
  findDeadRows,
  rateToZhe,
  resolveDiscount,
  splitList,
  summarizeGroup,
  untilToDateValue,
  zheToRate,
} from '../lib/match'
import type { DiscountRule } from '../types'

const now = 1_800_000_000

function rule(overrides: Partial<DiscountRule> = {}): DiscountRule {
  return { groups: 'vip', models: '*', rate: 0.8, until: 0, ...overrides }
}

describe('splitList / matchesList', () => {
  it('去空白、去空项、去重、保序', () => {
    expect(splitList(' vip , , svip ,vip ')).toEqual(['vip', 'svip'])
  })
})

describe('resolveDiscount', () => {
  const rules = [
    rule({ groups: 'vip', models: 'gpt-4o', rate: 0.7 }),
    rule({ groups: 'vip,svip', models: 'claude-3.7', rate: 0.85 }),
    rule({ groups: 'vip', models: '*', rate: 0.8 }),
  ]

  it('从上往下第一条命中即停', () => {
    expect(resolveDiscount(rules, 'vip', 'gpt-4o', now)).toEqual({
      index: 0,
      rate: 0.7,
    })
    expect(resolveDiscount(rules, 'vip', 'claude-3.7', now)).toEqual({
      index: 1,
      rate: 0.85,
    })
    expect(resolveDiscount(rules, 'vip', 'deepseek-r1', now)).toEqual({
      index: 2,
      rate: 0.8,
    })
  })

  it('多值分组命中任意一个即可', () => {
    expect(resolveDiscount(rules, 'svip', 'claude-3.7', now)).toEqual({
      index: 1,
      rate: 0.85,
    })
  })

  it('没有规则覆盖时返回 null（调用方回落组价）', () => {
    expect(resolveDiscount(rules, 'default', 'gpt-4o', now)).toBeNull()
  })

  it('跳过已过期规则，落到下一条', () => {
    const expired = [rule({ models: 'gpt-4o', rate: 0.5, until: now }), rule()]
    expect(resolveDiscount(expired, 'vip', 'gpt-4o', now)).toEqual({
      index: 1,
      rate: 0.8,
    })
  })

  it('跳过非法倍率，不把它当成 0 元', () => {
    const broken = [rule({ rate: 0 }), rule({ rate: 1 }), rule({ rate: 0.9 })]
    expect(resolveDiscount(broken, 'vip', 'gpt-4o', now)).toEqual({
      index: 2,
      rate: 0.9,
    })
  })

  it('空组名或空模型名不会命中', () => {
    expect(resolveDiscount(rules, '', 'gpt-4o', now)).toBeNull()
    expect(resolveDiscount(rules, 'vip', '', now)).toBeNull()
  })
})

describe('findDeadRows', () => {
  const groups = ['vip', 'svip']

  it('正常顺序下没有死行', () => {
    const rules = [
      rule({ groups: 'vip', models: 'gpt-4o', rate: 0.7 }),
      rule({ groups: 'vip', models: '*', rate: 0.8 }),
      rule({ groups: 'svip', models: '*', rate: 0.9 }),
    ]
    expect(findDeadRows(rules, groups, now)).toEqual([
      false,
      false,
      false,
    ])
  })

  it('兜底行被顶到最前面时，后面全部失效', () => {
    const rules = [
      rule({ groups: '*', models: '*', rate: 0.95 }),
      rule({ groups: 'vip', models: 'gpt-4o', rate: 0.7 }),
      rule({ groups: 'vip', models: '*', rate: 0.8 }),
      rule({ groups: 'svip', models: '*', rate: 0.9 }),
    ]
    expect(findDeadRows(rules, groups, now)).toEqual([
      false,
      true,
      true,
      true,
    ])
  })

  it('重复的兜底行只有第一条存活', () => {
    const rules = [
      rule({ groups: '*', models: '*', rate: 0.9 }),
      rule({ groups: '*', models: '*', rate: 0.95 }),
    ]
    expect(findDeadRows(rules, groups, now)).toEqual([false, true])
  })

  it('过期行不算死行（另有「已过期」提示）', () => {
    const rules = [rule({ until: now - 1 })]
    expect(findDeadRows(rules, groups, now)).toEqual([false])
  })

  it('指向不存在分组的行不算「被盖住」，由分组警告单独提示', () => {
    // 两种失败原因要分开说：死行是「顺序排错」，不存在的组是「组名写错」。
    // 混成一个标记，运营会去调顺序，而真正该改的是组名。
    const rules = [rule({ groups: 'not-exist' })]
    expect(findDeadRows(rules, groups, now)).toEqual([false])
  })

  it('点名超过 4 个模型时，靠后的模型同样参与判定', () => {
    // 旧实现只拿前 4 个点名模型当探针，第 5 个之后被完全盖住也看不出来
    const names = ['m1', 'm2', 'm3', 'm4', 'm5', 'm6']
    const rules = [
      rule({ groups: 'vip', models: '*' }),
      rule({ groups: 'vip', models: names.join(', ') }),
    ]
    expect(findDeadRows(rules, groups, now)).toEqual([false, true])
  })

  it('点名一个还没上架的模型，被 * 盖住也算死行', () => {
    // 判定不该依赖模型目录：目录里没有这个名字，规则照样永远不会命中
    const rules = [
      rule({ groups: 'vip', models: '*' }),
      rule({ groups: 'vip', models: 'brand-new-model' }),
    ]
    expect(findDeadRows(rules, groups, now)).toEqual([false, true])
  })

  it('* 规则只有被更早的 * 规则整体盖住才算死', () => {
    const rules = [
      rule({ groups: 'vip', models: 'gpt-4o' }),
      rule({ groups: 'vip', models: '*' }),
    ]
    expect(findDeadRows(rules, groups, now)).toEqual([false, false])
  })

  it('只盖住部分分组时不算死行', () => {
    const rules = [
      rule({ groups: 'vip', models: '*' }),
      rule({ groups: 'vip,svip', models: '*' }),
    ]
    expect(findDeadRows(rules, groups, now)).toEqual([false, false])
  })
})

describe('summarizeGroup', () => {
  const candidates = ['gpt-4o', 'claude-3.7', 'gemini-2.5']

  it('没有规则覆盖时只有一段组价，并清点剩余模型', () => {
    expect(summarizeGroup([], 'vip', candidates, 0.9, now)).toEqual([
      { kind: 'base', rate: 0.9, restCount: 3 },
    ])
  })

  it('点名模型先接管，后面的 * 收走剩余全部', () => {
    const rules = [
      rule({ models: 'gpt-4o', rate: 0.7 }),
      rule({ models: '*', rate: 0.8 }),
    ]
    expect(summarizeGroup(rules, 'vip', candidates, 0.9, now)).toEqual([
      {
        kind: 'rule',
        index: 0,
        rate: 0.7,
        models: ['gpt-4o'],
        catchesRest: false,
        until: 0,
      },
      {
        kind: 'rule',
        index: 1,
        rate: 0.8,
        models: [],
        catchesRest: true,
        until: 0,
      },
    ])
  })

  it('没有 * 规则时，未被点名的模型落到组价段', () => {
    const rules = [
      rule({ models: 'gpt-4o', rate: 0.7 }),
      rule({ models: 'claude-3.7', rate: 0.75 }),
    ]
    const segments = summarizeGroup(rules, 'vip', candidates, 0.9, now)

    expect(segments).toHaveLength(3)
    expect(segments.at(-1)).toEqual({ kind: 'base', rate: 0.9, restCount: 1 })
  })

  it('已被前面接管的模型不会在后面的行里重复出现', () => {
    const rules = [
      rule({ models: 'gpt-4o, claude-3.7', rate: 0.7 }),
      rule({ models: 'gpt-4o', rate: 0.5 }),
    ]
    const segments = summarizeGroup(rules, 'vip', candidates, 1, now)

    // 第 2 行点名的是第 1 行已经吃掉的模型，形同一行都不剩，不产生新段
    expect(segments).toHaveLength(2)
    expect(segments[0]).toMatchObject({ kind: 'rule', index: 0 })
    expect(segments[1]).toEqual({ kind: 'base', rate: 1, restCount: 1 })
  })

  it('过期与非法倍率的规则不参与分段', () => {
    const rules = [
      rule({ models: 'gpt-4o', rate: 0.5, until: now }),
      rule({ models: 'gpt-4o', rate: 0 }),
      rule({ models: 'gpt-4o', rate: 0.7 }),
    ]
    expect(summarizeGroup(rules, 'vip', candidates, 1, now)).toEqual([
      {
        kind: 'rule',
        index: 2,
        rate: 0.7,
        models: ['gpt-4o'],
        catchesRest: false,
        until: 0,
      },
      { kind: 'base', rate: 1, restCount: 2 },
    ])
  })

  it('模型量级不影响段数：1000 个模型仍只有规则数 + 1 段', () => {
    const many = Array.from({ length: 1000 }, (_, index) => `model-${index}`)
    const rules = [
      rule({ models: 'model-0, model-1', rate: 0.7 }),
      rule({ models: 'model-2', rate: 0.6 }),
    ]
    const segments = summarizeGroup(rules, 'vip', many, 1, now)

    expect(segments).toHaveLength(3)
    expect(segments.at(-1)).toEqual({ kind: 'base', rate: 1, restCount: 997 })
  })
})

describe('diagnoseRule', () => {
  const messages = {
    groupsRequired: 'groups',
    modelsRequired: 'models',
    rateRange: 'rate',
    groupUnknown: (group: string) => `unknown:${group}`,
    groupUnusable: (group: string) => `unusable:${group}`,
    modelUnknown: (list: string[]) => `model:${list.join('|')}`,
    priceIncrease: (group: string, baseRatio: number, rate: number) =>
      `increase:${group}:${baseRatio}:${rate}`,
    globalScope: 'global',
  }
  const context = {
    knownGroups: ['vip', 'svip', 'default'],
    usableGroups: ['vip', 'default'],
    knownModels: new Set(['gpt-4o', 'claude-3.7']),
    groupRatios: { vip: 0.9, svip: 0.8, default: 1 },
    nowSeconds: now,
    messages,
  }

  it('合法规则没有错误也没有警告', () => {
    const diagnosis = diagnoseRule(rule(), context)
    expect(diagnosis.errors).toEqual({})
    expect(diagnosis.warnings).toEqual([])
  })

  it('空分组、空模型、越界倍率都是阻断级错误', () => {
    expect(diagnoseRule(rule({ groups: '' }), context).errors.groups).toBe(
      'groups'
    )
    expect(diagnoseRule(rule({ models: ' ' }), context).errors.models).toBe(
      'models'
    )
    expect(diagnoseRule(rule({ rate: 0 }), context).errors.rate).toBe('rate')
    expect(diagnoseRule(rule({ rate: 1 }), context).errors.rate).toBe('rate')
  })

  it('分组不存在是警告，不是错误（能存，但永不命中）', () => {
    const diagnosis = diagnoseRule(rule({ groups: 'nope' }), context)
    expect(diagnosis.errors).toEqual({})
    expect(diagnosis.warnings).toEqual(['unknown:nope'])
  })

  it('该用户用不到的分组给出警告', () => {
    const diagnosis = diagnoseRule(rule({ groups: 'svip' }), context)
    expect(diagnosis.errors).toEqual({})
    expect(diagnosis.warnings).toEqual(['unusable:svip'])
  })

  it('* 不触发任何分组警告', () => {
    expect(
      diagnoseRule(rule({ groups: '*', models: 'gpt-4o' }), context).warnings
    ).toEqual([])
  })

  it('可用分组未知（空数组）时只校验存在性', () => {
    const diagnosis = diagnoseRule(rule({ groups: 'svip' }), {
      ...context,
      usableGroups: [],
    })
    expect(diagnosis.warnings).toEqual([])
  })

  it('规则倍率高于组价时提示涨价，而不是当成折扣', () => {
    // vip 组价 0.90，规则写 0.95：名义 9.5 折，实际比原价贵
    expect(diagnoseRule(rule({ rate: 0.95 }), context).warnings).toEqual([
      'increase:vip:0.9:0.95',
    ])
  })

  it('多分组时逐个比较，只有更贵的那个提示', () => {
    const diagnosis = diagnoseRule(
      rule({ groups: 'default,vip', rate: 0.95 }),
      context
    )
    // default 组价 1.00 → 0.95 是降价，不提示
    expect(diagnosis.warnings).toEqual(['increase:vip:0.9:0.95'])
  })

  it('同时写 */* 时提示影响面', () => {
    expect(
      diagnoseRule(rule({ groups: '*', models: '*' }), context).warnings
    ).toEqual(['global'])
  })

  it('模型名不在目录里时提示可能拼错', () => {
    const diagnosis = diagnoseRule(
      rule({ models: 'gpt-4o, gpt-4o-mini2' }),
      context
    )
    expect(diagnosis.warnings).toEqual(['model:gpt-4o-mini2'])
  })

  it('过期或被前面的行盖住时不再提涨价与拼写', () => {
    expect(
      diagnoseRule(rule({ rate: 0.95, until: now }), context).warnings
    ).toEqual([])
    expect(
      diagnoseRule(rule({ rate: 0.95 }), { ...context, dead: true }).warnings
    ).toEqual([])
  })
})

describe('折与倍率互转', () => {
  it('整数折与小数折都往返稳定', () => {
    for (const zhe of ['7', '7.5', '8', '9.5', '1', '9.9']) {
      expect(rateToZhe(zheToRate(zhe))).toBe(zhe)
    }
  })

  it('8 折存 0.8，7.5 折存 0.75', () => {
    expect(zheToRate('8')).toBe(0.8)
    expect(zheToRate('7.5')).toBe(0.75)
  })

  it('非法输入得到 NaN，调用方据此不更新', () => {
    expect(Number.isNaN(zheToRate(''))).toBe(true)
    expect(Number.isNaN(zheToRate('abc'))).toBe(true)
  })
})

describe('到期日期边界', () => {
  it('选 12/31 表示当天 23:59:59 失效，不是零点', () => {
    const until = dateValueToUntil('2026-12-31')
    const date = new Date(until * 1000)
    expect(date.getFullYear()).toBe(2026)
    expect(date.getMonth()).toBe(11)
    expect(date.getDate()).toBe(31)
    expect(date.getHours()).toBe(23)
    expect(date.getMinutes()).toBe(59)
    expect(date.getSeconds()).toBe(59)
  })

  it('日期与时间戳往返一致', () => {
    expect(untilToDateValue(dateValueToUntil('2027-01-05'))).toBe('2027-01-05')
  })

  it('空值表示永久', () => {
    expect(dateValueToUntil('')).toBe(0)
    expect(untilToDateValue(0)).toBe('')
  })
})
