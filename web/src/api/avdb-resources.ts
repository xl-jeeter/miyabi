import { keepPreviousData, useMutation, useQuery } from '@tanstack/react-query'

import { apiGet, apiPost } from './client'

export type AvdbCategoryCount = {
  name: string
  count: number
}

export type AvdbSection = {
  section: string
  count: number
  categories: AvdbCategoryCount[]
}

export type AvdbResource = {
  tid: string
  number: string
  title: string
  publish_date: string
  magnet: string
  preview_images: string
  detail_url: string
  size: number
  section: string
  category: string
  website: string
}

export type AvdbResourcePage = {
  items: AvdbResource[]
  total: number
  page: number
  limit: number
}

export type AvdbImportResult = {
  imported: number
  replaced: number
  skipped: number
  sections: number
  total: number
  source: string
}

export type AvdbSyncSource = 'x1080x' | 'sehuatang' | 'LTX' | 'FTAV'
export type AvdbSyncMode = 'full' | 'recent30d'

export type AvdbSyncPackage = {
  source: AvdbSyncSource
  mode: AvdbSyncMode
  name: string
  size: number
  download_url: string
  timestamp: string
  rows?: number
}

export type AvdbSyncState = {
  source: AvdbSyncSource
  mode: AvdbSyncMode
  package: string
  imported: number
  total: number
  synced_at: string
}

export type AvdbSyncStatus = {
  running: boolean
  current: string
  source?: AvdbSyncSource
  bytes: number
  total_bytes?: number
  phase?: 'downloading' | 'importing' | 'imported'
  imported?: number
  rows?: number
  last: AvdbSyncState[]
  result?: {
    job_id: string
    packages: AvdbSyncPackage[]
    imported: number
    total: number
    completed_at: string
  }
  last_error?: string
}

export type AvdbSyncRequest = {
  sources: AvdbSyncSource[]
  mode: AvdbSyncMode
}

export function useAvdbSections() {
  return useQuery({
    queryKey: ['avdb', 'sections'],
    queryFn: ({ signal }) => apiGet<AvdbSection[]>('/api/avdb/sections', undefined, signal),
    staleTime: 60_000
  })
}

export function useAvdbResources(params: {
  section?: string
  q?: string
  page: number
  limit: number
}) {
  return useQuery({
    queryKey: ['avdb', 'resources', params],
    placeholderData: keepPreviousData,
    queryFn: ({ signal }) =>
      apiGet<AvdbResourcePage>(
        '/api/avdb/resources',
        { section: params.section, q: params.q, page: params.page, limit: params.limit },
        signal
      )
  })
}

/** Uploads an AVDB export (.zip or .csv) and returns the import summary. */
export async function importAvdbExport(file: File): Promise<AvdbImportResult> {
  const form = new FormData()
  form.append('file', file)
  const response = await fetch('/api/avdb/import', {
    method: 'POST',
    body: form,
    credentials: 'include'
  })
  const payload: unknown = await response.json().catch(() => null)
  if (!response.ok) {
    const message =
      payload &&
      typeof payload === 'object' &&
      'error' in payload &&
      typeof payload.error === 'string'
        ? payload.error
        : `导入失败（HTTP ${response.status}）`
    throw new Error(message)
  }
  return payload as AvdbImportResult
}

export function startAvdbSync(request: AvdbSyncRequest) {
  return apiPost<{ job_id: string; packages: AvdbSyncPackage[] }>('/api/avdb/library/sync', request)
}

export function getAvdbSyncStatus(signal?: AbortSignal) {
  return apiGet<AvdbSyncStatus>('/api/avdb/library/sync/status', undefined, signal)
}

export function useAvdbSyncStatus(enabled = true) {
  return useQuery({
    queryKey: ['avdb', 'sync', 'status'],
    queryFn: ({ signal }) => getAvdbSyncStatus(signal),
    enabled,
    refetchInterval: query => (query.state.data?.running ? 750 : false),
    staleTime: 0
  })
}

export function useAvdbSyncPackages(enabled = true) {
  return useQuery({
    queryKey: ['avdb', 'sync', 'packages'],
    queryFn: ({ signal }) =>
      apiGet<AvdbSyncPackage[]>('/api/avdb/library/sync/packages', undefined, signal),
    enabled,
    staleTime: 60_000
  })
}

/** 取资源的第一张预览图（AVDB 的 preview_images 可能是 JSON 数组，也可能是逗号分隔的多个 URL）。 */
export function resourceImage(resource: AvdbResource): string {
  const raw = resource.preview_images?.trim()
  if (!raw) return ''
  if (raw.startsWith('[')) {
    try {
      const list: unknown = JSON.parse(raw)
      if (Array.isArray(list)) {
        for (const item of list) {
          if (typeof item === 'string' && item.trim()) return item.trim()
        }
      }
    } catch {
      return ''
    }
    return ''
  }
  for (const part of raw.split(/[,\n;]/)) {
    const candidate = part.trim()
    if (/^https?:\/\//i.test(candidate)) return candidate
  }
  return ''
}

/** 把导入资源的磁力直接提交到 115 离线。 */
export function useResourceOffline() {
  return useMutation({
    mutationFn: (tid: string) =>
      apiPost<{ task_id: string }>(`/api/avdb/resources/${encodeURIComponent(tid)}/offline`)
  })
}

/** AVDB 资源图片走专用代理（通用 /api/image 只允许 JavDB 域名）。 */
export function avdbImageURL(source: string): string {
  if (!source) return ''
  return `/api/avdb/image?url=${encodeURIComponent(source)}`
}
