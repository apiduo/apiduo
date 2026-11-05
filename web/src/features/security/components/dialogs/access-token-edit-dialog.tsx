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
import { zodResolver } from '@hookform/resolvers/zod'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { PermissionMatrix } from '@/components/permission-matrix'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  permissionMatrixToScopes,
  scopesToPermissionMatrix,
} from '@/lib/admin-permissions'

import type {
  AccessTokenCatalog,
  AccessTokenItem,
  AccessTokenUpdate,
} from '../../api'
import { accessTokenPermissionGroups } from '../../lib/access-token-catalog'
import {
  accessTokenEditFormSchema,
  type AccessTokenEditFormValues,
} from '../../lib/access-token-schema'

type AccessTokenEditDialogProps = {
  token: AccessTokenItem
  // Hides the dialog while security verification is showing, keeping the form.
  hidden: boolean
  catalog: AccessTokenCatalog
  pending: boolean
  onClose: () => void
  onSave: (input: AccessTokenUpdate) => Promise<boolean>
}

export function AccessTokenEditDialog(props: AccessTokenEditDialogProps) {
  const { t } = useTranslation()
  const groups = accessTokenPermissionGroups(props.catalog)
  // Only scopes the catalog still offers are editable. Saving with the
  // permissions untouched renames the token and keeps its stored grant.
  const offered = new Set(
    groups.flatMap((group) =>
      group.resources.flatMap((resource) =>
        resource.actions.map(
          (option) => `${resource.resource}:${option.action}`
        )
      )
    )
  )
  const initialScopes = props.token.scopes
    .filter((scope) => offered.has(scope))
    .sort()
  // APIDUO:token-ip-allowlist:BEGIN
  const initialAllowIps = (props.token.allow_ips ?? '').trim()
  const [allowIps, setAllowIps] = useState(initialAllowIps)
  // APIDUO:token-ip-allowlist:END
  const form = useForm<AccessTokenEditFormValues>({
    resolver: zodResolver(accessTokenEditFormSchema),
    defaultValues: {
      name: props.token.name,
      permissions: scopesToPermissionMatrix(initialScopes),
    },
  })

  const close = () => {
    if (!props.pending) props.onClose()
  }
  const submit = async (values: AccessTokenEditFormValues) => {
    const scopes = permissionMatrixToScopes(values.permissions)
    const changed = scopes.join(' ') !== initialScopes.join(' ')
    if (changed && scopes.length === 0) {
      form.setError('permissions', {
        message: 'Select at least one permission',
      })
      return
    }
    // APIDUO:token-ip-allowlist:BEGIN Sent only when it actually changed, so a
    // rename never rewrites the restriction by accident.
    const nextAllowIps = allowIps.trim()
    const ipsChanged = nextAllowIps !== initialAllowIps
    // APIDUO:token-ip-allowlist:END
    const saved = await props.onSave({
      name: values.name.trim(),
      scopes: changed ? scopes : undefined,
      allow_ips: ipsChanged ? nextAllowIps : undefined,
    })
    if (saved) props.onClose()
  }

  return (
    <Dialog
      open={!props.hidden}
      onOpenChange={(open) => {
        if (!open) close()
      }}
      title={t('Edit access token')}
      contentClassName='sm:max-w-2xl'
      contentHeight='auto'
      footer={
        <>
          <p className='text-muted-foreground text-xs sm:mr-auto sm:self-center'>
            {t('Saving permission changes requires security verification')}
          </p>
          <Button
            type='button'
            variant='outline'
            disabled={props.pending}
            onClick={close}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form='access-token-edit-form'
            disabled={props.pending}
          >
            {t('Save')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id='access-token-edit-form'
          className='space-y-5 py-2'
          onSubmit={form.handleSubmit(submit)}
        >
          <FormField
            control={form.control}
            name='name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Token name')}</FormLabel>
                <FormControl>
                  <Input {...field} autoComplete='off' />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
                    {/* APIDUO:token-ip-allowlist:BEGIN */}
          <div className='space-y-1.5'>
            <label
              className='text-sm font-medium'
              htmlFor='access-token-edit-allow-ips'
            >
              {t('Allowed addresses (optional)')}
            </label>
            <textarea
              id='access-token-edit-allow-ips'
              className='border-input placeholder:text-muted-foreground focus-visible:ring-ring min-h-16 w-full rounded-md border bg-transparent px-3 py-2 font-mono text-xs shadow-xs outline-none focus-visible:ring-1'
              value={allowIps}
              placeholder={'10.0.0.0/8\n203.0.113.7'}
              onChange={(event) => setAllowIps(event.target.value)}
            />
            <p className='text-muted-foreground text-[11px] leading-relaxed'>
              {t(
                'One address or CIDR block per line. Leave empty to allow any address. Pinning a token to the network it runs on is the cheapest way to contain a leak: a copied key stops working elsewhere.'
              )}
            </p>
          </div>
          {/* APIDUO:token-ip-allowlist:END */}
<FormField
            control={form.control}
            name='permissions'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Permissions')}</FormLabel>
                <PermissionMatrix
                  groups={groups}
                  value={field.value}
                  disabled={props.pending}
                  onChange={field.onChange}
                />
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
