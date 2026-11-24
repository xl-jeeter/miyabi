import type { BrowseMoviesParams } from '@/api/discover'
import { useDiscoverMovies } from '@/api/discover'
import { DiscoverResults } from './results'

export function BrowseResults({
  params,
  onPageChange
}: {
  params: BrowseMoviesParams & { page: number }
  onPageChange: (page: number) => void
}) {
  const movies = useDiscoverMovies(params)
  return (
    <DiscoverResults
      movies={movies.data}
      loading={movies.isPending}
      fetching={movies.isFetching}
      error={movies.error}
      searching={false}
      page={params.page}
      onPageChange={onPageChange}
      onRetry={() => movies.refetch()}
    />
  )
}
