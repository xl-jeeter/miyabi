import { CalendarClockIcon, LoaderCircleIcon, RefreshCwIcon } from 'lucide-react'
import { toast } from 'sonner'

import {
  useAvdbAutoUpdateStatus,
  useRunAvdbAutoUpdateNow,
  useUpdateAvdbAutoUpdate
} from '@/api/avdb-auto-update'
import { useAvdbSyncStatus } from '@/api/avdb-resources'
import { describeApiError } from '@/api/client'
import { InlineError } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { SettingRow, SettingsSection } from './shared'

const intervalOptions = [1, 2, 4, 8, 12, 24] as const

export function AvdbAutoUpdateSection() {
  const status = useAvdbAutoUpdateStatus()
  const syncStatus = useAvdbSyncStatus()
  const update = useUpdateAvdbAutoUpdate()
  const runNow = useRunAvdbAutoUpdateNow()

  const config = status.data
  const disabled = status.isLoading || update.isPending
  const running = syncStatus.data?.running === true
  const busy = running || runNow.isPending

  const progressText = (() => {
    const current = syncStatus.data
    if (!current?.running) return null
    const target = current.source ? `${current.source} 的 ${current.current}` : current.current
    if (current.phase === 'importing') {
      return `正在导入 ${formatCount(current.rows ?? current.imported ?? 0)} 行（${target}）`
    }
    return `正在下载 ${target}`
  })()

  function save(patch: Partial<NonNullable<typeof config>>) {
    if (!config) return
    update.mutate(
      {
        enabled: config.enabled,
        interval_hours: config.interval_hours,
        sources: config.sources,
        notify: config.notify,
        ...patch
      },
      {
        onSuccess: () => toast.success('资源库自动更新设置已保存'),
        onError: error => toast.error(describeApiError(error))
      }
    )
  }

  function toggleSource(source: string) {
    if (!config) return
    const selected = config.sources.includes(source)
      ? config.sources.filter(item => item !== source)
      : [...config.sources, source]
    save({ sources: selected })
  }

  return (
    <SettingsSection icon={<CalendarClockIcon className="size-4" />} title="AVDB 资源库自动更新">
      <SettingRow
        title="定时在线更新"
        description="按设定间隔自动从在线发布包同步 AVDB 资源库"
        inline
      >
        <Switch
          checked={config?.enabled ?? true}
          disabled={disabled}
          onCheckedChange={enabled => save({ enabled })}
        />
      </SettingRow>

      <SettingRow title="更新间隔" description="两次自动更新之间的时间间隔">
        <Select
          value={String(config?.interval_hours ?? 12)}
          disabled={disabled}
          onValueChange={value => save({ interval_hours: Number(value) })}
        >
          <SelectTrigger className="w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {intervalOptions.map(hours => (
              <SelectItem key={hours} value={String(hours)}>
                每 {hours} 小时
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </SettingRow>

      <SettingRow
        title="数据源"
        description="留空表示同步全部数据源；也可只保留需要的部分以加快更新"
      >
        <div className="flex flex-wrap justify-end gap-2">
          {(config?.package_sources ?? []).map(source => (
            <label
              key={source}
              className="flex cursor-pointer items-center gap-2 rounded-xl border px-3 py-2 text-sm"
            >
              <Checkbox
                checked={config?.sources.includes(source) ?? false}
                disabled={disabled}
                onCheckedChange={() => toggleSource(source)}
              />
              <span>{source}</span>
            </label>
          ))}
          {status.isSuccess && !config?.package_sources.length ? (
            <span className="text-xs text-muted-foreground">暂无可用数据源</span>
          ) : null}
        </div>
      </SettingRow>

      <SettingRow title="完成通知" description="自动更新完成后在日志中记录一条摘要" inline>
        <Switch
          checked={config?.notify ?? true}
          disabled={disabled}
          onCheckedChange={notify => save({ notify })}
        />
      </SettingRow>

      <SettingRow title="立即更新" description="用当前保存的配置马上同步一次，并重新开始计时间隔">
        <div className="space-y-2 text-right">
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() =>
              runNow.mutate(undefined, {
                onSuccess: () => toast.success('已开始更新，完成后会记录结果'),
                onError: error => toast.error(describeApiError(error))
              })
            }
          >
            {busy ? (
              <LoaderCircleIcon className="size-4 animate-spin" />
            ) : (
              <RefreshCwIcon className="size-4" />
            )}
            {running ? '更新中…' : '立即更新'}
          </Button>
          {progressText ? <p className="text-xs text-muted-foreground">{progressText}</p> : null}
          {config?.last_run_at ? (
            <p className="text-xs text-muted-foreground">
              上次更新：{formatTime(config.last_run_at)}
              {config.last_result ? `（${config.last_result}）` : ''}
            </p>
          ) : null}
          {config?.enabled && config.next_run_at ? (
            <p className="text-xs text-muted-foreground">
              下次更新：{formatTime(config.next_run_at)}
            </p>
          ) : null}
        </div>
      </SettingRow>

      {status.isError ? (
        <InlineError onRetry={() => void status.refetch()} retrying={status.isFetching}>
          暂时无法读取自动更新设置，请检查后端服务后重试。
        </InlineError>
      ) : null}
    </SettingsSection>
  )
}

function formatTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

function formatCount(count: number): string {
  return count.toLocaleString('zh-CN')
}
