import { useEffect, useMemo, useRef, useState } from 'react'

import {
  startAvdbSync,
  useAvdbSyncPackages,
  useAvdbSyncStatus,
  type AvdbSyncMode,
  type AvdbSyncSource
} from '@/api/avdb-resources'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle
} from '@/components/ui/dialog'
import { RefreshCwIcon } from 'lucide-react'

const SOURCES: Array<{ id: AvdbSyncSource; label: string }> = [
  { id: 'x1080x', label: 'X1080X' },
  { id: 'sehuatang', label: 'sehuatang' },
  { id: 'LTX', label: 'LTX' },
  { id: 'FTAV', label: 'FTAV' }
]

function formatCount(count: number): string {
  return count.toLocaleString('zh-CN')
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / 1024 ** index
  return `${value >= 100 || index === 0 ? Math.round(value) : value.toFixed(1)} ${units[index]}`
}

function formatSyncTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const pad = (part: number) => String(part).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function LastAvdbSyncUpdate() {
  const status = useAvdbSyncStatus()
  const latest = useMemo(
    () =>
      [...(status.data?.last ?? [])].sort((left, right) =>
        right.synced_at.localeCompare(left.synced_at)
      )[0],
    [status.data?.last]
  )
  if (!latest) return null
  return (
    <div className="text-sm text-muted-foreground">
      上次更新：{formatSyncTime(latest.synced_at)}（{latest.package}）
    </div>
  )
}

export function ResourceOnlineSync({ onCompleted }: { onCompleted: () => void }) {
  const [open, setOpen] = useState(false)
  const [selected, setSelected] = useState<AvdbSyncSource[]>(() => SOURCES.map(source => source.id))
  const [mode, setMode] = useState<AvdbSyncMode>('full')
  const [submitting, setSubmitting] = useState(false)
  const [message, setMessage] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const activeJobID = useRef<string | null>(null)
  const wasRunning = useRef(false)

  const status = useAvdbSyncStatus()
  const packages = useAvdbSyncPackages()

  useEffect(() => {
    const current = status.data
    if (!current) return
    if (current.running) {
      wasRunning.current = true
      return
    }
    if (!wasRunning.current) return
    wasRunning.current = false
    if (current.last_error) {
      setError(current.last_error)
      return
    }
    if (current.result && current.result.job_id === activeJobID.current) {
      setMessage(
        `本次在线更新完成：导入 ${formatCount(current.result.imported)} 条，库内合计 ${formatCount(current.result.total)} 条。`
      )
      activeJobID.current = null
      onCompleted()
    }
  }, [onCompleted, status.data])

  const selectedPackages = useMemo(
    () =>
      (packages.data ?? []).filter(item => item.mode === mode && selected.includes(item.source)),
    [mode, packages.data, selected]
  )
  const hasUpdates = selectedPackages.some(item => {
    const previous = status.data?.last.find(state => state.source === item.source)
    return previous?.mode !== item.mode || previous.package !== item.name
  })
  const running = status.data?.running || submitting

  const toggleSource = (source: AvdbSyncSource) => {
    setSelected(current =>
      current.includes(source) ? current.filter(item => item !== source) : [...current, source]
    )
  }

  const submit = async () => {
    if (!selected.length) return
    setSubmitting(true)
    setMessage(null)
    setError(null)
    try {
      const job = await startAvdbSync({ sources: selected, mode })
      activeJobID.current = job.job_id
      wasRunning.current = true
      await status.refetch()
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : '在线更新启动失败')
    } finally {
      setSubmitting(false)
    }
  }

  const progressText = (() => {
    const current = status.data
    if (!current?.running) return null
    const target = current.source ? `${current.source} 的 ${current.current}` : current.current
    if (current.phase === 'importing') {
      return `正在导入 ${formatCount(current.rows ?? current.imported ?? 0)} 行（${target}）`
    }
    return `正在下载 ${target}（${formatBytes(current.bytes)} / ${formatBytes(current.total_bytes ?? 0)}）`
  })()

  return (
    <>
      <Button variant="outline" onClick={() => setOpen(true)} disabled={running}>
        <RefreshCwIcon className={running ? 'animate-spin' : undefined} />
        在线更新
        {hasUpdates && (
          <span className="rounded-full bg-sky-100 px-1.5 text-xs text-sky-700 dark:bg-sky-950 dark:text-sky-300">
            有新数据
          </span>
        )}
      </Button>

      {(progressText || message || error) && (
        <div
          className={[
            'rounded-xl border px-4 py-3 text-sm',
            error
              ? 'border-red-200 bg-red-50 text-red-900 dark:border-red-900 dark:bg-red-950 dark:text-red-100'
              : 'border-sky-200 bg-sky-50 text-sky-900 dark:border-sky-900 dark:bg-sky-950 dark:text-sky-100'
          ].join(' ')}
        >
          {progressText ?? error ?? message}
        </div>
      )}

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>在线更新 AVDB 数据</DialogTitle>
            <DialogDescription>
              从最新发布包选择数据源；大包会先流式下载到临时文件，再逐个导入。
            </DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-4">
            <div className="grid grid-cols-2 gap-3">
              {SOURCES.map(source => (
                <label
                  key={source.id}
                  className="flex cursor-pointer items-center gap-2 rounded-xl border px-3 py-2.5"
                >
                  <Checkbox
                    checked={selected.includes(source.id)}
                    onCheckedChange={() => toggleSource(source.id)}
                  />
                  <span>{source.label}</span>
                </label>
              ))}
            </div>

            <div className="flex gap-2">
              <Button
                type="button"
                variant={mode === 'full' ? 'default' : 'outline'}
                onClick={() => setMode('full')}
              >
                全量
              </Button>
              <Button
                type="button"
                variant={mode === 'recent30d' ? 'default' : 'outline'}
                onClick={() => setMode('recent30d')}
              >
                近 30 天
              </Button>
            </div>

            <div className="max-h-40 overflow-y-auto rounded-xl border">
              {packages.isPending ? (
                <div className="px-3 py-3 text-sm text-muted-foreground">正在读取发布包…</div>
              ) : packages.isError ? (
                <div className="px-3 py-3 text-sm text-red-600">发布包清单读取失败</div>
              ) : selectedPackages.length ? (
                selectedPackages.map(item => (
                  <div
                    key={`${item.source}-${item.mode}`}
                    className="flex items-center justify-between gap-3 border-b px-3 py-2 last:border-0"
                  >
                    <span className="truncate text-xs">{item.name}</span>
                    <span className="shrink-0 text-xs text-muted-foreground">
                      {formatBytes(item.size)}
                    </span>
                  </div>
                ))
              ) : (
                <div className="px-3 py-3 text-sm text-muted-foreground">
                  当前条件下没有可选数据包
                </div>
              )}
            </div>

            {progressText && <div className="text-sm text-muted-foreground">{progressText}</div>}
            {error && <div className="text-sm text-red-600">{error}</div>}
            {message && <div className="text-sm text-emerald-700">{message}</div>}
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              关闭
            </Button>
            <Button
              onClick={() => void submit()}
              disabled={running || !selected.length || !selectedPackages.length}
            >
              {submitting || status.data?.running ? '更新中…' : '开始更新'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
