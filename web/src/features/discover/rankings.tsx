import { useQuery } from '@tanstack/react-query'
import { useContext, useState } from 'react'

import { apiGet } from '@/api/client'
import type { DiscoverMovie } from '@/api/discover'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { MovieCard, MovieGridLayout, MovieGridSkeleton } from '@/components/movie'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { MovieDetailDialogContext } from '@/features/movie-detail/dialog-context'

const PERIODS = [
  { value: 'daily', label: '今日' },
  { value: 'weekly', label: '本周' },
  { value: 'monthly', label: '本月' }
] as const

type Period = (typeof PERIODS)[number]['value']

/**
 * AVDB 只给自家 ID，而详情/磁力/一键离线都挂在本站 JavDB 目录上，
 * 所以点卡片时先用番号在 JavDB 解析出真实 ID 再打开详情。
 */
function RankingCard({
  movie,
  onNotice
}: {
  movie: DiscoverMovie
  onNotice: (message: string) => void
}) {
  const openMovie = useContext(MovieDetailDialogContext)
  const [busy, setBusy] = useState(false)

  const open = async (event: React.MouseEvent<HTMLDivElement>) => {
    if (!openMovie || busy) return
    event.preventDefault()
    setBusy(true)
    try {
      const resolved = await apiGet<{ id: string }>('/api/discover/movies/resolve', {
        code: movie.code
      })
      openMovie({ id: resolved.id }, event.currentTarget)
    } catch {
      onNotice(`JavDB 里查不到 ${movie.code}，无法打开详情与离线`)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={event => void open(event)}
      onKeyDown={event => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault()
          event.currentTarget.click()
        }
      }}
      className="cursor-pointer rounded-2xl outline-ring"
    >
      <MovieCard movie={movie} description={movie.release_date} />
      {busy && <span className="sr-only">正在解析番号</span>}
    </div>
  )
}

/**
 * AVDB 榜单：数据来自外部 AVDB 实例（/discover/rankings 后端代理），
 * 卡片与网格复用发现页现有组件。
 */
export function RankingsContent() {
  const [period, setPeriod] = useState<Period>('daily')
  const [notice, setNotice] = useState<string | null>(null)
  const query = useQuery({
    queryKey: ['discover', 'rankings', period],
    queryFn: ({ signal }) =>
      apiGet<DiscoverMovie[]>('/api/discover/rankings', { period }, signal),
    staleTime: 5 * 60_000
  })

  return (
    <div className="flex flex-col gap-6">
      <Tabs value={period} onValueChange={value => setPeriod(value as Period)}>
        <TabsList>
          {PERIODS.map(item => (
            <TabsTrigger key={item.value} value={item.value}>
              {item.label}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>

      {notice && (
        <div className="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-100">
          {notice}
        </div>
      )}

      {query.isError ? (
        <ErrorState
          message="榜单加载失败，请检查 AVDB 数据源配置"
          onRetry={() => query.refetch()}
          retrying={query.isFetching}
        />
      ) : query.isPending ? (
        <MovieGridSkeleton count={20} />
      ) : query.data.length === 0 ? (
        <EmptyState title="暂无榜单数据" />
      ) : (
        <MovieGridLayout>
          {query.data.map(movie => (
            <RankingCard key={movie.id} movie={movie} onNotice={setNotice} />
          ))}
        </MovieGridLayout>
      )}
    </div>
  )
}
