import { describe, expect, it } from 'vitest'
import { parseLogOther } from '../format'

describe('parseLogOther model mapping lifting', () => {
  it('lifts the admin-only fields from current rows', () => {
    const parsed = parseLogOther(
      JSON.stringify({
        admin_info: {
          is_model_mapped: true,
          upstream_model_name: 'deepseek-v3-250101',
          response_model: {
            requested_model: 'acme-pro',
            upstream_model: 'deepseek-v3-250101',
            returned_model: 'deepseek-v3-250101',
          },
        },
      })
    )

    expect(parsed?.is_model_mapped).toBe(true)
    expect(parsed?.upstream_model_name).toBe('deepseek-v3-250101')
    expect(parsed?.response_model?.upstream_model).toBe('deepseek-v3-250101')
  })

  it('keeps the legacy top-level shape unchanged', () => {
    const parsed = parseLogOther(
      JSON.stringify({
        is_model_mapped: true,
        upstream_model_name: 'deepseek-v3-250101',
      })
    )

    expect(parsed?.is_model_mapped).toBe(true)
    expect(parsed?.upstream_model_name).toBe('deepseek-v3-250101')
  })

  it('exposes nothing for the user projection', () => {
    // The backend strips admin_info and the legacy keys for non-admins.
    const parsed = parseLogOther('{}')
    expect(parsed?.is_model_mapped).toBeUndefined()
    expect(parsed?.upstream_model_name).toBeUndefined()
    expect(parsed?.response_model).toBeUndefined()
  })

  it('prefers the top level when both shapes are present', () => {
    const parsed = parseLogOther(
      JSON.stringify({
        is_model_mapped: true,
        upstream_model_name: 'legacy-name',
        admin_info: { upstream_model_name: 'admin-name' },
      })
    )

    expect(parsed?.upstream_model_name).toBe('legacy-name')
  })

  it('does not fabricate fields when admin_info is unrelated', () => {
    const parsed = parseLogOther(
      JSON.stringify({ admin_info: { is_multi_key: true } })
    )
    expect(parsed?.upstream_model_name).toBeUndefined()
  })
})
