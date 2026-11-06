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
import { useQuery } from '@tanstack/react-query'
import { KeyRound, Plus, ShieldCheck, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'

import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { PermissionMatrix } from '@/components/permission-matrix'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Dialog } from '@/components/dialog'
import { Input } from '@/components/ui/input'
import { LoadingState } from '@/components/loading-state'
import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { permissionMatrixToScopes } from '@/lib/admin-permissions'

import { getAccessTokenCatalog } from '../api'
import { useAgentKeys } from '../hooks/use-agent-keys'
import { accessTokenPermissionGroups } from '../lib/access-token-catalog'

// APIDUO:agent-key:BEGIN
/**
 * Agent keys let an automated client authenticate with a private key instead of
 * a bearer token.
 *
 * The console only ever receives the public half, so nothing stored here is a
 * secret. Signing happens on the client: it requests a challenge, signs it, and
 * trades the signature for a short-lived access token.
 */
export function AgentKeysCard() {
  const { t, i18n } = useTranslation()
  const keys = useAgentKeys()
  const catalog = useQuery({
    queryKey: ['security', 'agent-keys', 'catalog'],
    queryFn: () => getAccessTokenCatalog(),
    retry: false,
  })

  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const formatTime = (seconds: number) =>
    new Intl.DateTimeFormat(locale, {
      dateStyle: 'medium',
      timeStyle: 'short',
    }).format(new Date(seconds * 1000))

  const [createOpen, setCreateOpen] = useState(false)
  const [revokeTarget, setRevokeTarget] = useState<{ id: number; name: string } | null>(null)

  const groups = useMemo(
    () =>
      catalog.data ? accessTokenPermissionGroups(catalog.data) : [],
    [catalog.data]
  )

  const items = keys.list.data ?? []
  const atLimit = items.length >= keys.maxKeys

  const confirmRevoke = async () => {
    const target = revokeTarget
    if (!target || keys.pending) return
    setRevokeTarget(null)
    await keys.revoke(target.id)
  }

  let content
  if (keys.list.isPending) {
    content = (
      <div role='status'>
        <LoadingState size='sm' className='min-h-24' />
      </div>
    )
  } else if (keys.list.isError) {
    content = (
      <p className='text-destructive text-sm'>
        {t('Failed to load agent keys')}
      </p>
    )
  } else if (items.length === 0) {
    content = (
      <div className='text-muted-foreground flex flex-col items-center gap-2 rounded-lg border border-dashed px-4 py-8 text-center text-sm'>
        <KeyRound className='size-6 opacity-60' aria-hidden />
        <p>{t('No agent keys yet.')}</p>
        <p className='text-xs'>
          {t(
            'Register a public key and hand the private half to your agent. The private key never leaves the machine that signs with it.'
          )}
        </p>
      </div>
    )
  } else {
    content = (
      <ul className='divide-y rounded-lg border'>
        {items.map((item) => {
          const state = agentKeyState(item)
          return (
            <li
              key={item.id}
              className='flex flex-wrap items-start justify-between gap-3 px-3 py-2.5'
            >
              <div className='min-w-0 flex-1 space-y-1'>
                <div className='flex flex-wrap items-center gap-2'>
                  <span className='truncate text-sm font-medium'>{item.name}</span>
                  <span className={AGENT_KEY_STATE_CLASS[state]}>
                    {AGENT_KEY_STATE_LABEL[state](t)}
                  </span>
                  <span className='text-muted-foreground font-mono text-[11px]'>
                    {item.key_type}
                  </span>
                </div>
                <div className='text-muted-foreground flex flex-wrap gap-x-3 gap-y-0.5 text-[11px]'>
                  <span>
                    {item.last_used_at
                      ? t('Last used {{time}}', {
                          time: formatTime(item.last_used_at),
                        })
                      : t('Never used')}
                    {item.last_used_ip ? ` · ${item.last_used_ip}` : ''}
                  </span>
                  {item.expires_at !== 0 && (
                    <span>
                      {t('Expires {{time}}', {
                        time: formatTime(item.expires_at),
                      })}
                    </span>
                  )}
                </div>
                <div className='text-muted-foreground truncate font-mono text-[11px] opacity-70'>
                  {item.public_key.slice(0, 28)}
                  {item.public_key.length > 28 ? '…' : ''}
                </div>
              </div>
              {state !== 'revoked' && (
                <Button
                  size='sm'
                  variant='ghost'
                  className='text-destructive hover:text-destructive'
                  disabled={keys.pending}
                  onClick={() => setRevokeTarget({ id: item.id, name: item.name })}
                >
                  <Trash2 data-icon='inline-start' />
                  {t('Revoke')}
                </Button>
              )}
            </li>
          )
        })}
      </ul>
    )
  }

  return (
    <>
      <Card
        data-card-hover='false'
        data-apiduo={apiduoBlockMark('security:agent-keys')}
      >
        <CardHeader>
          <CardTitle>{t('Agent keys')}</CardTitle>
          <CardDescription>
            {t(
              'Sign a challenge with a private key to obtain a short-lived token. Preferred over a static token for automated clients: a leaked token expires within minutes, and a revoked key stops working immediately.'
            )}
          </CardDescription>
          <CardAction className='flex flex-wrap justify-end gap-2'>
            <Button
              size='sm'
              disabled={atLimit || keys.pending || !catalog.data}
              onClick={() => setCreateOpen(true)}
            >
              <Plus data-icon='inline-start' />
              {t('Register agent key')}
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent className='space-y-2'>
          {atLimit && (
            <p className='text-muted-foreground text-xs'>
              {t('You can register up to {{count}} agent keys', {
                count: keys.maxKeys,
              })}
            </p>
          )}
          {content}
          <p className='text-muted-foreground flex items-start gap-1.5 pt-1 text-[11px] leading-relaxed'>
            <ShieldCheck className='mt-0.5 size-3.5 shrink-0' aria-hidden />
            {t(
              'Only the public key is stored, so a database dump cannot be used to impersonate an agent. A password change revokes every key at once.'
            )}
          </p>
        </CardContent>
      </Card>

      {catalog.data && (
        <AgentKeyCreateDialog
          open={createOpen}
          hidden={keys.showVerification}
          groups={groups}
          pending={keys.pending}
          onOpenChange={setCreateOpen}
          onCreate={keys.create}
        />
      )}

      <Dialog
        open={revokeTarget !== null}
        onOpenChange={(open) => {
          if (!open) setRevokeTarget(null)
        }}
        title={t('Revoke agent key')}
        description={t('This cannot be undone.')}
        footer={
          <>
            <Button variant='outline' onClick={() => setRevokeTarget(null)}>
              {t('Cancel')}
            </Button>
            <Button
              variant='destructive'
              disabled={keys.pending}
              onClick={confirmRevoke}
            >
              {t('Revoke')}
            </Button>
          </>
        }
      >
        <p className='text-sm'>
          {revokeTarget
            ? t(
                '{{name}} will stop working immediately, and any token it issued is deleted.',
                { name: revokeTarget.name }
              )
            : ''}
        </p>
      </Dialog>

      <SecureVerificationDialog {...keys.verificationDialogProps} />
    </>
  )
}

type AgentKeyState = 'active' | 'revoked' | 'expired'

/** A key is usable until it is revoked; expiry is the secondary axis. */
function agentKeyState(item: { revoked_at: number; expires_at: number }): AgentKeyState {
  if (item.revoked_at !== 0) return 'revoked'
  if (item.expires_at !== 0 && item.expires_at * 1000 < Date.now()) return 'expired'
  return 'active'
}

const AGENT_KEY_STATE_CLASS: Record<AgentKeyState, string> = {
  active:
    'rounded-full bg-emerald-500/10 px-2 py-0.5 text-[11px] text-emerald-600 dark:text-emerald-400',
  revoked: 'rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground',
  expired: 'rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground',
}

const AGENT_KEY_STATE_LABEL: Record<
  AgentKeyState,
  (t: (key: string) => string) => string
> = {
  active: (t) => t('Active'),
  revoked: (t) => t('Revoked'),
  expired: (t) => t('Expired'),
}

type AgentKeyCreateDialogProps = {
  open: boolean
  /** Hides the dialog while the verification prompt is showing. */
  hidden: boolean
  groups: ReturnType<typeof accessTokenPermissionGroups>
  pending: boolean
  onOpenChange: (open: boolean) => void
  onCreate: (input: {
    name: string
    public_key: string
    scopes: string[]
    expires_at: number
  }) => Promise<boolean>
}

function AgentKeyCreateDialog(props: AgentKeyCreateDialogProps) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [publicKey, setPublicKey] = useState('')
  const [permissions, setPermissions] = useState({})
  const [error, setError] = useState('')

  const reset = () => {
    setName('')
    setPublicKey('')
    setPermissions({})
    setError('')
  }

  const submit = async () => {
    const scopes = permissionMatrixToScopes(permissions)
    if (!name.trim()) {
      setError(t('Enter a name'))
      return
    }
    if (!publicKey.trim()) {
      setError(t('Paste the public key'))
      return
    }
    if (scopes.length === 0) {
      setError(t('Select at least one permission'))
      return
    }
    setError('')
    const ok = await props.onCreate({
      name: name.trim(),
      public_key: publicKey.trim(),
      scopes,
      // Keys do not expire by default: rotating the key pair is the operator's
      // decision, and an accidental expiry silently breaks a running agent.
      expires_at: 0,
    })
    if (ok) {
      reset()
      props.onOpenChange(false)
    }
  }

  return (
    <Dialog
      open={props.open && !props.hidden}
      onOpenChange={(open) => {
        if (!open) reset()
        props.onOpenChange(open)
      }}
      title={t('Register agent key')}
      description={t(
        'Paste the public half of an Ed25519 key pair. Generate one with: openssl genpkey -algorithm ed25519 -out agent.pem'
      )}
      contentClassName='max-h-[70vh] overflow-y-auto'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button disabled={props.pending} onClick={submit}>
            {t('Register')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <div className='space-y-1.5'>
          <label className='text-sm font-medium' htmlFor='agent-key-name'>
            {t('Name')}
          </label>
          <Input
            id='agent-key-name'
            value={name}
            placeholder='workbuddy'
            onChange={(event) => setName(event.target.value)}
          />
        </div>

        <div className='space-y-1.5'>
          <label className='text-sm font-medium' htmlFor='agent-key-public'>
            {t('Public key')}
          </label>
          <textarea
            id='agent-key-public'
            className='border-input placeholder:text-muted-foreground focus-visible:ring-ring min-h-24 w-full rounded-md border bg-transparent px-3 py-2 font-mono text-xs shadow-xs outline-none focus-visible:ring-1'
            value={publicKey}
            placeholder={'-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----'}
            onChange={(event) => setPublicKey(event.target.value)}
          />
          <p className='text-muted-foreground text-[11px]'>
            {t(
              'Accepted: a PEM public key block, or the raw key as base64. The private key stays on the client and is never sent here.'
            )}
          </p>
        </div>

        <div className='space-y-1.5'>
          <span className='text-sm font-medium'>{t('Permissions')}</span>
          <PermissionMatrix
            groups={props.groups}
            value={permissions}
            disabled={props.pending}
            onChange={setPermissions}
          />
        </div>

        {error && <p className='text-destructive text-sm'>{error}</p>}
      </div>
    </Dialog>
  )
}
// APIDUO:agent-key:END
