import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { apiGet, apiPost } from '@/api/client'
import { discoverKeys } from '@/api/movie-detail-cache'

export type JavDBAccount = {
  logged_in: boolean
  username: string
}

export const javdbAccountKeys = {
  account: ['javdb', 'account'] as const
}

export function useJavDBAccount() {
  return useQuery({
    queryKey: javdbAccountKeys.account,
    queryFn: ({ signal }) => apiGet<JavDBAccount>('/api/javdb/account', undefined, signal),
    staleTime: 30_000
  })
}

function useJavDBAccountMutation<T>(mutationFn: (variables: T) => Promise<JavDBAccount>) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn,
    onSuccess: account => {
      queryClient.setQueryData(javdbAccountKeys.account, account)
      void queryClient.invalidateQueries({ queryKey: discoverKeys.all })
    }
  })
}

export function useJavDBLogin() {
  return useJavDBAccountMutation((credentials: { username: string; password: string }) =>
    apiPost<JavDBAccount>('/api/javdb/account/login', credentials)
  )
}

export function useJavDBLogout() {
  return useJavDBAccountMutation(() => apiPost<JavDBAccount>('/api/javdb/account/logout'))
}
