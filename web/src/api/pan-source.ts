import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { apiGet, apiPost, apiPut } from './client'

export type PanProvider = '115' | 'cd2'

// The backend never returns the token or the password, only whether they are set.
export type PanSourceCD2 = {
  endpoint: string
  account: string
  username: string
  hasToken: boolean
  hasPassword: boolean
}

export type PanSourceStatus = {
  provider: PanProvider
  effectiveProvider: PanProvider
  requiresRestart: boolean
  cd2: PanSourceCD2
}

// Empty fields keep the stored value, so a page without the secrets can save.
export type PanSourceSettings = {
  provider: PanProvider
  cd2: {
    endpoint: string
    token: string
    username: string
    password: string
    account: string
  }
}

export type PanSourceTestResponse = {
  ok: boolean
  latencyMs?: number
  accounts?: { id: string; name: string }[]
  error?: string
}

export const panSourceKeys = {
  status: ['settings', 'pan-source'] as const
}

export function usePanSourceStatus() {
  return useQuery({
    queryKey: panSourceKeys.status,
    queryFn: ({ signal }) => apiGet<PanSourceStatus>('/api/settings/pan-source', undefined, signal),
    staleTime: 15_000
  })
}

export function useUpdatePanSource() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (settings: PanSourceSettings) =>
      apiPut<PanSourceStatus>('/api/settings/pan-source', settings),
    onSuccess: status => {
      queryClient.setQueryData(panSourceKeys.status, status)
    }
  })
}

export function useTestPanSource() {
  return useMutation({
    mutationFn: (candidate: Partial<PanSourceSettings['cd2']>) =>
      apiPost<PanSourceTestResponse>('/api/settings/pan-source/test', candidate)
  })
}
