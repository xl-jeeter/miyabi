import { useQueryClient } from '@tanstack/react-query'
import { useRef, useState } from 'react'

import {
  avdbImageURL,
  importAvdbExport,
  resourceImage,
  useAvdbResources,
  useAvdbSections,
  useResourceOffline,
  type AvdbImportResult,
  type AvdbResource
} from '@/api/avdb-resources'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'

const PAGE_SIZE = 40

// 马卡龙色系，和 AVDB 的分类卡片视觉保持一致。
const CARD_COLORS = [
  'bg-sky-100 text-sky-900 dark:bg-sky-950 dark:text-sky-100',
  'bg-emerald-100 text-emerald-900 dark:bg-emerald-950 dark:text-emerald-100',
  'bg-violet-100 text-violet-900 dark:bg-violet-950 dark:text-violet-100',
  'bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-100',
  'bg-rose-100 text-rose-900 dark:bg-rose-950 dark:text-rose-100',
  'bg-cyan-100 text-cyan-900 dark:bg-cyan-950 dark:text-cyan-100',
  'bg-lime-100 text-lime-900 dark:bg-lime-950 dark:text-lime-100',
  'bg-indigo-100 text-indigo-900 dark:bg-indigo-950 dark:text-indigo-100'
]

function formatCount(count: number): string {
  return count.toLocaleString('zh-CN')
}

/** 一条资源的操作按钮组：磁力 / 原帖 / 115 离线。窄屏和宽屏共用。 */
function ResourceActions({
  item,
  offline,
  onNotice
}: {
  item: AvdbResource
  offline: ReturnType<typeof useResourceOffline>
  onNotice: (message: string) => void
}) {
  return (
    <>
      {item.magnet && (
        <a
          href={item.magnet}
          className="whitespace-nowrap text-xs text-sky-600 underline-offset-2 hover:underline dark:text-sky-400"
          target="_blank"
          rel="noreferrer"
        >
          磁力
        </a>
      )}
      {item.detail_url && (
        <a
          href={item.detail_url}
          className="whitespace-nowrap text-xs text-sky-600 underline-offset-2 hover:underline dark:text-sky-400"
          target="_blank"
          rel="noreferrer"
        >
          原帖
        </a>
      )}
      <Button
        size="sm"
        variant="outline"
        className="whitespace-nowrap"
        disabled={!item.magnet || offline.isPending}
        onClick={() =>
          void offline
            .mutateAsync(item.tid)
            .then(() => onNotice(`已提交 115 离线：${item.number || item.tid}`))
            .catch(error => {
              const message = error instanceof Error ? error.message : '离线提交失败'
              onNotice(
                /115|账号|PAN_UNAUTHORIZED/i.test(message) ? '请先在设置里登录 115 账号' : message
              )
            })
        }
      >
        {offline.isPending && offline.variables === item.tid ? '提交中…' : '115 离线'}
      </Button>
    </>
  )
}

/**
 * AVDB 资源库：展示导入后的分区卡片（名称 + 条目数），点开看该分区的资源列表。
 * 数据完全来自导入的 AVDB 导出文件，不依赖 AVDB 在线。
 */
export function ResourcesContent() {
  const queryClient = useQueryClient()
  const sections = useAvdbSections()
  const [activeSection, setActiveSection] = useState<string | undefined>(undefined)
  const [keyword, setKeyword] = useState('')
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const [importing, setImporting] = useState(false)
  const [importResult, setImportResult] = useState<AvdbImportResult | null>(null)
  const [importError, setImportError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const offline = useResourceOffline()
  const fileInput = useRef<HTMLInputElement>(null)

  const resources = useAvdbResources({ section: activeSection, q: search, page, limit: PAGE_SIZE })

  const pickSection = (section?: string) => {
    setActiveSection(section)
    setPage(1)
  }

  const onFileChange = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file) return
    setImporting(true)
    setImportError(null)
    setImportResult(null)
    try {
      const result = await importAvdbExport(file)
      setImportResult(result)
      await queryClient.invalidateQueries({ queryKey: ['avdb'] })
      setPage(1)
    } catch (error) {
      setImportError(error instanceof Error ? error.message : '导入失败')
    } finally {
      setImporting(false)
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center gap-3">
        <Input
          value={keyword}
          onChange={event => setKeyword(event.target.value)}
          onKeyDown={event => {
            if (event.key === 'Enter') {
              setSearch(keyword.trim())
              setPage(1)
            }
          }}
          placeholder="按番号或标题搜索资源库"
          className="max-w-sm"
        />
        <Button
          variant="outline"
          onClick={() => {
            setSearch(keyword.trim())
            setPage(1)
          }}
        >
          搜索
        </Button>
        <div className="ml-auto flex items-center gap-3">
          {importing && <span className="text-sm text-muted-foreground">正在导入…</span>}
          <Button onClick={() => fileInput.current?.click()} disabled={importing}>
            导入 AVDB 导出文件
          </Button>
          <input
            ref={fileInput}
            type="file"
            accept=".zip,.csv"
            hidden
            onChange={file => void onFileChange(file)}
          />
        </div>
      </div>

      {importResult && (
        <div className="rounded-xl border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-100">
          导入完成：共 <b>{formatCount(importResult.imported)}</b> 条，库内合计{' '}
          <b>{formatCount(importResult.total)}</b> 条，{importResult.sections} 个分区，跳过{' '}
          {formatCount(importResult.skipped)} 条。
        </div>
      )}
      {importError && (
        <div className="rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-900 dark:border-red-900 dark:bg-red-950 dark:text-red-100">
          {importError}
        </div>
      )}
      {notice && (
        <div className="rounded-xl border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-900 dark:border-sky-900 dark:bg-sky-950 dark:text-sky-100">
          {notice}
        </div>
      )}

      {sections.isPending ? (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          {Array.from({ length: 8 }).map((_, index) => (
            <Skeleton key={index} className="h-24 rounded-2xl" />
          ))}
        </div>
      ) : sections.isError ? (
        <ErrorState
          message="资源库读取失败"
          onRetry={() => void sections.refetch()}
          retrying={sections.isFetching}
        />
      ) : sections.data.length === 0 ? (
        <EmptyState title="还没有导入数据，导入 AVDB 导出文件后这里会出现分区卡片。" />
      ) : (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          {sections.data.map((item, index) => {
            const active = item.section === activeSection
            return (
              <button
                key={item.section}
                type="button"
                onClick={() => pickSection(active ? undefined : item.section)}
                className={[
                  'flex flex-col items-start gap-2 rounded-2xl px-4 py-4 text-left transition',
                  CARD_COLORS[index % CARD_COLORS.length],
                  active ? 'ring-2 ring-foreground/40' : 'hover:brightness-[0.97]'
                ].join(' ')}
              >
                <span className="text-sm font-medium opacity-80">{item.section}</span>
                <span className="text-2xl font-semibold tabular-nums">{formatCount(item.count)}</span>
                {item.categories?.length ? (
                  <span className="line-clamp-1 text-xs opacity-60">
                    {item.categories
                      .slice(0, 3)
                      .map(category => category.name)
                      .join(' / ')}
                  </span>
                ) : null}
              </button>
            )
          })}
        </div>
      )}

      <div className="flex items-center justify-between">
        <div className="text-sm text-muted-foreground">
          {activeSection ? `分区：${activeSection}` : '全部资源'}
          {resources.data && ` · 共 ${formatCount(resources.data.total)} 条`}
        </div>
        {activeSection && (
          <Button variant="ghost" size="sm" onClick={() => pickSection(undefined)}>
            清除分区筛选
          </Button>
        )}
      </div>

      {resources.isPending ? (
        <div className="flex flex-col gap-2">
          {Array.from({ length: 6 }).map((_, index) => (
            <Skeleton key={index} className="h-14 rounded-lg" />
          ))}
        </div>
      ) : resources.isError ? (
        <ErrorState
          message="资源列表加载失败"
          onRetry={() => void resources.refetch()}
          retrying={resources.isFetching}
        />
      ) : !resources.data.items?.length ? (
        <EmptyState title="没有匹配的资源" />
      ) : (
        <div className="flex flex-col divide-y rounded-xl border">
          {resources.data.items.map(item => (
            <div key={item.tid} className="flex items-start gap-3 px-3 py-2.5">
              <img
                src={avdbImageURL(resourceImage(item))}
                alt=""
                loading="lazy"
                onError={event => {
                  event.currentTarget.style.visibility = 'hidden'
                }}
                className="h-16 w-24 shrink-0 rounded-md bg-muted object-cover sm:h-20 sm:w-32"
              />
              <div className="flex min-w-0 flex-1 flex-col gap-1">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="whitespace-nowrap font-mono text-sm font-medium">
                    {item.number || '—'}
                  </span>
                  <span className="whitespace-nowrap text-xs text-muted-foreground">
                    {item.publish_date?.slice(0, 10)}
                  </span>
                  <span className="whitespace-nowrap rounded-md bg-muted px-1.5 py-0.5 text-xs">
                    {item.section}
                  </span>
                </div>
                <span className="line-clamp-2 text-sm text-muted-foreground sm:text-foreground">
                  {item.title}
                </span>
                {/* 窄屏：操作按钮放在文字下方，避免把中间挤成一条缝 */}
                <div className="mt-1 flex flex-wrap items-center gap-3 sm:hidden">
                  <ResourceActions item={item} offline={offline} onNotice={setNotice} />
                </div>
              </div>
              <div className="hidden shrink-0 items-center gap-3 sm:flex">
                <ResourceActions item={item} offline={offline} onNotice={setNotice} />
              </div>
            </div>
          ))}
        </div>
      )}

      {resources.data && resources.data.total > PAGE_SIZE && (
        <div className="flex items-center justify-center gap-3">
          <Button
            variant="outline"
            size="sm"
            disabled={page <= 1}
            onClick={() => setPage(current => Math.max(1, current - 1))}
          >
            上一页
          </Button>
          <span className="text-sm text-muted-foreground">
            {page} / {Math.max(1, Math.ceil(resources.data.total / PAGE_SIZE))}
          </span>
          <Button
            variant="outline"
            size="sm"
            disabled={page >= Math.ceil(resources.data.total / PAGE_SIZE)}
            onClick={() => setPage(current => current + 1)}
          >
            下一页
          </Button>
        </div>
      )}
    </div>
  )
}
