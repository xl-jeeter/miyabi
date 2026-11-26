import { useState } from 'react'
import { DatabaseIcon, LoaderCircleIcon, NetworkIcon, PlugZapIcon } from 'lucide-react'
import { toast } from 'sonner'

import { describeApiError } from '@/api/client'
import {
  type PanProvider,
  type PanSourceSettings,
  type PanSourceStatus,
  usePanSourceStatus,
  useTestPanSource,
  useUpdatePanSource
} from '@/api/pan-source'
import { InlineError } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { cn } from 'cn'
import { SettingRow, SettingsSection } from './shared'

const providers: { id: PanProvider; title: string; description: string }[] = [
  { id: '115', title: '115 网盘', description: '使用 115 官方 API，扫码登录' },
  { id: 'cd2', title: 'CloudDrive2', description: '通过 CloudDrive2 读取云盘内容' }
]

// The form mirrors the persisted fields. Secrets start empty: the backend only
// reports whether they are stored, and an empty field means "keep it".
type CD2Form = PanSourceSettings['cd2']

const emptyForm: CD2Form = {
  endpoint: '',
  token: '',
  username: '',
  password: '',
  account: ''
}

export function PanSourceSection() {
  const status = usePanSourceStatus()
  const update = useUpdatePanSource()
  const test = useTestPanSource()

  const saved = status.data
  const [userProvider, setUserProvider] = useState<PanProvider | null>(null)
  const [userForm, setUserForm] = useState<Partial<CD2Form>>({})
  const [restartHint, setRestartHint] = useState(false)
  const [testResult, setTestResult] = useState<
    { ok: true; summary: string } | { ok: false; error: string } | null
  >(null)

  const provider = userProvider ?? saved?.provider ?? '115'
  const form: CD2Form = { ...savedForm(saved), ...userForm }
  const disabled = status.isPending || status.isError || update.isPending

  function setField(field: keyof CD2Form, value: string) {
    setUserForm(current => ({ ...current, [field]: value }))
    setTestResult(null)
  }

  function handleProviderChange(next: PanProvider) {
    if (next === provider) return
    setUserProvider(next)
    setTestResult(null)
  }

  function handleTest() {
    test.mutate(form, {
      onSuccess: result => {
        if (!result.ok) {
          setTestResult({ ok: false, error: result.error || '无法连接 CloudDrive2' })
          return
        }
        const count = result.accounts?.length ?? 0
        setTestResult({
          ok: true,
          summary: `连接正常 · ${count} 个账号 · ${result.latencyMs ?? 0}ms`
        })
      },
      onError: error => setTestResult({ ok: false, error: describeApiError(error) })
    })
  }

  function handleSave() {
    update.mutate(
      { provider, cd2: form },
      {
        onSuccess: result => {
          setUserProvider(null)
          setUserForm({})
          setRestartHint(result.requiresRestart)
          toast.success(result.requiresRestart ? '已保存，重启容器后生效' : '数据源设置已保存')
        },
        onError: error => toast.error(describeApiError(error))
      }
    )
  }

  const pendingRestart = restartHint || (saved?.requiresRestart ?? false)

  return (
    <SettingsSection icon={<DatabaseIcon className="size-4" />} title="数据源">
      <div className="grid gap-3 sm:grid-cols-2">
        {providers.map(item => {
          const active = provider === item.id
          return (
            <button
              key={item.id}
              type="button"
              disabled={disabled}
              aria-pressed={active}
              onClick={() => handleProviderChange(item.id)}
              className={cn(
                'flex cursor-pointer items-start gap-3 rounded-2xl border px-4 py-3 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-50',
                active
                  ? 'border-primary bg-primary/5 ring-1 ring-primary'
                  : 'border-border hover:bg-muted/50'
              )}
            >
              <NetworkIcon
                className={cn('mt-0.5 size-4', active ? 'text-primary' : 'text-muted-foreground')}
              />
              <div className="min-w-0 space-y-1">
                <div className="text-sm font-medium">{item.title}</div>
                <div className="text-xs leading-5 text-muted-foreground">{item.description}</div>
              </div>
            </button>
          )
        })}
      </div>

      {provider === 'cd2' ? (
        <Card className="gap-5 border-none bg-muted/30 px-4 py-4 ring-1 ring-foreground/5">
          <SettingRow title="接入地址" description="CloudDrive2 的 gRPC 地址，https 会启用 TLS">
            <Input
              type="text"
              value={form.endpoint}
              placeholder="http://192.168.1.10:19798"
              disabled={disabled}
              onChange={e => setField('endpoint', e.target.value)}
            />
          </SettingRow>

          <SettingRow title="API Token" description="留空表示不修改已保存的 Token">
            <Input
              type="password"
              autoComplete="new-password"
              value={form.token}
              placeholder={
                saved?.cd2.hasToken ? '已设置，留空不修改' : 'CloudDrive2 中生成的 API Token'
              }
              disabled={disabled}
              onChange={e => setField('token', e.target.value)}
            />
          </SettingRow>

          <SettingRow title="账号" description="多账号时必填，格式如 115open:80005515">
            <Input
              type="text"
              value={form.account}
              placeholder="115open:80005515"
              disabled={disabled}
              onChange={e => setField('account', e.target.value)}
            />
          </SettingRow>

          <details className="group space-y-5">
            <summary className="cursor-pointer text-xs font-medium text-muted-foreground select-none">
              使用用户名 + 密码（可选）
            </summary>
            <p className="pt-2 text-xs leading-5 text-muted-foreground">
              不给 Token 时才用账密换 JWT，密码留空表示不修改。
            </p>
            <SettingRow title="用户名" description="CloudDrive2 的登录用户名">
              <Input
                type="text"
                autoComplete="username"
                value={form.username}
                placeholder={saved?.cd2.username || 'CloudDrive2 用户名'}
                disabled={disabled}
                onChange={e => setField('username', e.target.value)}
              />
            </SettingRow>
            <SettingRow title="密码" description="留空表示不修改已保存的密码">
              <Input
                type="password"
                autoComplete="new-password"
                value={form.password}
                placeholder={saved?.cd2.hasPassword ? '已设置，留空不修改' : 'CloudDrive2 密码'}
                disabled={disabled}
                onChange={e => setField('password', e.target.value)}
              />
            </SettingRow>
          </details>

          {testResult ? (
            testResult.ok ? (
              <p className="text-sm text-emerald-700 dark:text-emerald-400">{testResult.summary}</p>
            ) : (
              <p className="text-sm text-destructive">{testResult.error}</p>
            )
          ) : null}

          <div className="flex items-center justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={disabled || test.isPending}
              onClick={handleTest}
            >
              {test.isPending ? (
                <LoaderCircleIcon className="animate-spin" />
              ) : (
                <PlugZapIcon className="size-4" />
              )}
              测试连接
            </Button>
            <Button type="button" size="sm" disabled={disabled} onClick={handleSave}>
              {update.isPending ? <LoaderCircleIcon className="animate-spin" /> : null}
              保存
            </Button>
          </div>
        </Card>
      ) : (
        <p className="text-xs leading-5 text-muted-foreground">
          使用 115 官方 API，扫码登录在下方。
        </p>
      )}

      {pendingRestart ? (
        <div className="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-100">
          已保存，重启容器后生效。
          {saved?.requiresRestart
            ? `当前运行的数据源仍是「${providerLabel(saved.effectiveProvider)}」。`
            : ''}
        </div>
      ) : null}

      {status.isError ? (
        <InlineError onRetry={() => void status.refetch()} retrying={status.isFetching}>
          暂时无法读取数据源设置，请检查后端服务后重试。
        </InlineError>
      ) : null}
    </SettingsSection>
  )
}

// savedForm seeds the form from the stored values without the secrets.
function savedForm(status: PanSourceStatus | undefined): CD2Form {
  return {
    ...emptyForm,
    endpoint: status?.cd2.endpoint ?? '',
    account: status?.cd2.account ?? '',
    username: status?.cd2.username ?? ''
  }
}

function providerLabel(provider: PanProvider): string {
  return provider === 'cd2' ? 'CloudDrive2' : '115 网盘'
}
