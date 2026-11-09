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
import { Link, useSearch } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { TermsFooter } from '@/features/auth/components/terms-footer'
import { UserAuthForm } from '@/features/auth/sign-in/components/user-auth-form'
import { useStatus } from '@/hooks/use-status'
import { useSystemConfig } from '@/hooks/use-system-config'

import './skins.css'
import {
  readSkinOverride,
  resolveLoginSkin,
} from './skin'

/** 官网一致的三方列表，纯展示，不参与任何登录逻辑。 */
const MODEL_NAMES = [
  'OpenAI',
  'Anthropic',
  'Gemini',
  'DeepSeek',
  'Qwen',
  'Grok',
  'Mistral',
]

/**
 * apiduo 登录页。
 *
 * 结构上只有一份 DOM，六套皮肤靠根节点的 `data-login-skin` 换 `skins.css` 里的
 * 一组变量与少量结构性覆盖。表单本身仍然是上游的 `UserAuthForm`——登录牵扯
 * 二次验证、通行密钥、Turnstile、协议同意，重写这些换视觉不划算，皮肤只负责
 * 壳与控件观感。
 *
 * 皮肤来源优先级：`?theme=`（预览）> `/api/status` 的 `login_theme` > aurora。
 */
export function ApiduoSignIn() {
  const { t } = useTranslation()
  const { redirect } = useSearch({ from: '/(auth)/sign-in' })
  const { status } = useStatus()
  const { systemName, logo, loading } = useSystemConfig()

  // `login_theme` 是新字段，上游的 SystemStatus 类型里没有，这里就地取窄类型，
  // 避免为一个可选字段去改上游的类型声明。
  const configured = (
    status as
      | { login_theme?: string; data?: { login_theme?: string } }
      | null
      | undefined
  )
  const skin = resolveLoginSkin(
    configured?.login_theme ?? configured?.data?.login_theme,
    readSkinOverride()
  )

  return (
    <div
      className='apiduo-login'
      data-login-skin={skin}
      data-apiduo='login-skin'
    >
      <div className='al-bg' aria-hidden='true'>
        <div className='al-bg__orb al-bg__orb--a' />
        <div className='al-bg__orb al-bg__orb--b' />
        <div className='al-bg__orb al-bg__orb--c' />
        <div className='al-bg__grid' />
        <div className='al-bg__floor' />
        <div className='al-bg__vig' />
      </div>

      <header className='al-top'>
        <Link to='/' className='al-lockup'>
          <span className='al-mark'>
            {loading ? (
              <span className='al-mark__ph' />
            ) : (
              <img src={logo} alt='' width={34} height={34} />
            )}
          </span>
          <span className='al-name'>{systemName}</span>
          <span className='al-tag'>AI GATEWAY</span>
        </Link>
        <nav className='al-nav'>
          <a href='#'>文档</a>
          <a href='#' className='al-nav__status'>
            <i />
            服务状态
          </a>
        </nav>
      </header>

      <main className='al-main'>
        <section className='al-hero'>
          <p className='al-eyebrow'>
            <i />
            多模型统一接入<em>· 单一密钥</em>
          </p>

          <h1 className='al-title'>
            一个密钥，
            <br />
            <span className='al-grad'>连通所有模型</span>
          </h1>

          <p className='al-lede'>
            统一协议接入 40+ 主流大模型。
            <br />
            智能路由、故障转移，用量与费用实时可见。
          </p>

          {/* aurora / light / cyber：请求链路 */}
          <div className='al-flow' aria-hidden='true'>
            <div className='al-flow__row'>
              <span className='al-node'>
                <i className='a' />
                你的应用
              </span>
              <span className='al-rail'>
                <span className='al-spark' />
                <span className='al-spark al-spark--2' />
              </span>
              <span className='al-node al-node--core'>
                <img src={logo} alt='' />
                统一路由
              </span>
              <span className='al-rail'>
                <span className='al-spark' />
                <span className='al-spark al-spark--2' />
              </span>
              <span className='al-node'>
                <i className='c' />
                40+ 模型
              </span>
            </div>
            <div className='al-flow__foot'>
              <code>POST /v1/chat/completions</code>
              <span className='al-sep' />
              <span className='al-ok'>200</span>
              <span>SSE</span>
            </div>
          </div>

          {/* terminal：命令行示例 */}
          <div className='al-term' aria-hidden='true'>
            <div className='al-term__bar'>
              <i />
              <i />
              <i />
              <span>bash — api.apiduo.cn</span>
            </div>
            <div className='al-term__body'>
              <div>
                <span className='p'>$</span> curl
                https://api.apiduo.cn/v1/chat/completions \
              </div>
              <div className='al-ind'>
                -H <span className='k'>&quot;Authorization: Bearer sk-••••&quot;</span>{' '}
                \
              </div>
              <div className='al-ind'>
                -d{' '}
                <span className='k'>{`'{"model":"gpt-5.2","stream":true}'`}</span>
              </div>
              <div className='al-gap' />
              <div>
                <span className='ok'>✓ 200</span> routed →{' '}
                <span className='s'>openai/gpt-5.2</span> fallback{' '}
                <span className='c'>claude-opus-4.6</span>
              </div>
              <div className='c'>→ tokens 1,284 · cost ¥0.0031</div>
              <div>
                <span className='p'>$</span> <span className='al-caret' />
              </div>
            </div>
          </div>

          {/* editorial：三条编号说明 */}
          <div className='al-notes'>
            <div className='al-note'>
              <span className='al-note__no'>01</span>
              <div>
                <div className='al-note__t'>统一协议</div>
                <div className='al-note__d'>
                  OpenAI 兼容，切换模型只改一个字段。
                </div>
              </div>
            </div>
            <div className='al-note'>
              <span className='al-note__no'>02</span>
              <div>
                <div className='al-note__t'>智能路由与故障转移</div>
                <div className='al-note__d'>
                  上游 5xx 或超时，自动落到备用线路，调用方无感。
                </div>
              </div>
            </div>
            <div className='al-note'>
              <span className='al-note__no'>03</span>
              <div>
                <div className='al-note__t'>用量透明</div>
                <div className='al-note__d'>
                  每次调用都有记录：模型、token、耗时、金额。
                </div>
              </div>
            </div>
          </div>

          {/* warm：三点口语说明 */}
          <ul className='al-points'>
            <li>
              <span className='al-dot' />
              <div>
                <div className='al-note__t'>少维护一套东西</div>
                <div className='al-note__d'>
                  密钥、配额、重试、计费，网关里都替你管好了。
                </div>
              </div>
            </li>
            <li>
              <span className='al-dot' />
              <div>
                <div className='al-note__t'>上游挂了不掉线</div>
                <div className='al-note__d'>
                  自动切到备用线路，调用方不用改代码。
                </div>
              </div>
            </li>
            <li>
              <span className='al-dot' />
              <div>
                <div className='al-note__t'>每一笔都看得见</div>
                <div className='al-note__d'>
                  按模型、按成员拆分的用量与费用明细。
                </div>
              </div>
            </li>
          </ul>

          <div className='al-models'>
            {MODEL_NAMES.map((name, index) => (
              <span key={name}>
                {index > 0 && <i />}
                {name}
              </span>
            ))}
          </div>
        </section>

        <section className='al-panel'>
          <div className='al-mhead'>
            <span className='al-mark'>
              {loading ? (
                <span className='al-mark__ph' />
              ) : (
                <img src={logo} alt='' width={40} height={40} />
              )}
            </span>
            <span className='al-name'>{systemName}</span>
          </div>

          <div className='al-card'>
            <div className='al-card__meta'>
              <span>SECURE CHANNEL · TLS 1.3</span>
              <span className='al-live'>
                <i />
                ONLINE
              </span>
            </div>

            <h2 className='al-card__title'>{t('Sign in')}</h2>
            <p className='al-card__sub'>统一接入 40+ 模型，一个账号通行</p>

            <UserAuthForm redirectTo={redirect} />

            {!status?.self_use_mode_enabled &&
              status?.register_enabled !== false && (
                <p className='al-card__foot'>
                  {t("Don't have an account?")}{' '}
                  <Link to='/sign-up'>{t('Sign up')}</Link>
                </p>
              )}

            <TermsFooter
              variant='sign-in'
              status={status}
              className='al-terms'
            />
          </div>
        </section>
      </main>
    </div>
  )
}
