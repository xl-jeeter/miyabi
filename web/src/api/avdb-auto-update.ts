import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { apiGet, apiPost, apiPut } from './client'

export type AvdbAutoUpdateSettings = {
  enabled: boolean
  interval_hours: number
  sources: string[]
  notify: boolean
}

export type AvdbAutoUpdateStatus = AvdbAutoUpdateSettings & {
  last_run_at: string
  last_result: string
  running: boolean
  next_run_at: string
  package_sources: string[]
}

export const avdbAutoUpdateKeys = {
  all: ['avdb', 'auto-update'] as const
}

export function useAvdbAutoUpdateStatus() {
  return useQuery({
    queryKey: avdbAutoUpdateKeys.all,
    queryFn: ({ signal }) =>
      apiGet<AvdbAutoUpdateStatus>('/api/avdb/library/auto-update', undefined, signal),
    staleTime: 10_000
  })
}

export function useUpdateAvdbAutoUpdate() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (settings: AvdbAutoUpdateSettings) =>
      apiPut<AvdbAutoUpdateSettings>('/api/avdb/library/auto-update', settings),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: avdbAutoUpdateKeys.all })
    }
  })
}

export function useRunAvdbAutoUpdateNow() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => apiPost<{ started: boolean }>('/api/avdb/library/auto-update/run-now'),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: avdbAutoUpdateKeys.all })
      // Kick the sync-status poller so live progress shows immediately.
      void queryClient.invalidateQueries({ queryKey: ['avdb', 'sync', 'status'] })
    }
  })
}
