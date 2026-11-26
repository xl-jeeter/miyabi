import { renderToStaticMarkup } from 'react-dom/server'
import { expect, test, vi } from 'vitest'

import { type PanSourceStatus, usePanSourceStatus } from '@/api/pan-source'
import { usePanAccount } from '@/api/pan'
import { PanSection } from '@/features/settings/pan-section'
import { PanSourceSection } from '@/features/settings/pan-source-section'

vi.mock('@/api/pan-source', () => ({
  usePanSourceStatus: vi.fn(),
  useUpdatePanSource: vi.fn(),
  useTestPanSource: vi.fn()
}))

vi.mock('@/api/pan', () => ({
  usePanAccount: vi.fn(),
  useDisconnectPan: vi.fn(() => ({
    mutate: vi.fn(),
    isPending: false,
    isError: false
  }))
}))

function mockStatus(status: PanSourceStatus) {
  vi.mocked(usePanSourceStatus).mockReturnValue({
    data: status,
    isPending: false,
    isError: false,
    isFetching: false,
    refetch: vi.fn()
  } as unknown as ReturnType<typeof usePanSourceStatus>)
}

function mockMutations() {
  const mutation = { mutate: vi.fn(), isPending: false }
  return mutation
}

const cd2Status: PanSourceStatus = {
  provider: 'cd2',
  effectiveProvider: 'cd2',
  requiresRestart: false,
  cd2: {
    endpoint: 'http://192.168.1.10:19798',
    account: '115open:80005515',
    username: '',
    hasToken: true,
    hasPassword: false
  }
}

test('CloudDrive2 shows the form and never asks for the stored token again', async () => {
  const { useUpdatePanSource, useTestPanSource } = await import('@/api/pan-source')
  vi.mocked(useUpdatePanSource).mockReturnValue(
    mockMutations() as unknown as ReturnType<typeof useUpdatePanSource>
  )
  vi.mocked(useTestPanSource).mockReturnValue(
    mockMutations() as unknown as ReturnType<typeof useTestPanSource>
  )
  mockStatus(cd2Status)

  const html = renderToStaticMarkup(<PanSourceSection />)
  expect(html).toContain('已设置，留空不修改')
  expect(html).toContain('http://192.168.1.10:19798')
  expect(html).toContain('115open:80005515')
  expect(html).toContain('测试连接')
  expect(html).not.toContain('扫码登录在下方')
})

test('115 hides the CloudDrive2 form and points at the QR login', async () => {
  const { useUpdatePanSource, useTestPanSource } = await import('@/api/pan-source')
  vi.mocked(useUpdatePanSource).mockReturnValue(
    mockMutations() as unknown as ReturnType<typeof useUpdatePanSource>
  )
  vi.mocked(useTestPanSource).mockReturnValue(
    mockMutations() as unknown as ReturnType<typeof useTestPanSource>
  )
  mockStatus({ ...cd2Status, provider: '115', effectiveProvider: '115' })

  const html = renderToStaticMarkup(<PanSourceSection />)
  expect(html).toContain('使用 115 官方 API，扫码登录在下方')
  expect(html).not.toContain('API Token')
  expect(html).not.toContain('测试连接')
})

test('a pending restart is called out until the container is restarted', async () => {
  const { useUpdatePanSource, useTestPanSource } = await import('@/api/pan-source')
  vi.mocked(useUpdatePanSource).mockReturnValue(
    mockMutations() as unknown as ReturnType<typeof useUpdatePanSource>
  )
  vi.mocked(useTestPanSource).mockReturnValue(
    mockMutations() as unknown as ReturnType<typeof useTestPanSource>
  )
  mockStatus({ ...cd2Status, provider: '115', requiresRestart: true })

  const html = renderToStaticMarkup(<PanSourceSection />)
  expect(html).toContain('已保存，重启容器后生效')
  expect(html).toContain('当前运行的数据源仍是「CloudDrive2」')
})

test('the 115 login section is disabled while CloudDrive2 is the source', () => {
  vi.mocked(usePanAccount).mockReturnValue({
    data: undefined,
    isPending: false,
    isError: false,
    isFetching: false,
    refetch: vi.fn()
  } as unknown as ReturnType<typeof usePanAccount>)
  mockStatus(cd2Status)

  const html = renderToStaticMarkup(<PanSection />)
  expect(html).toContain('当前数据源是 CloudDrive2，115 账号请在 CD2 中管理')
  expect(html).not.toContain('扫码登录')
  // The account query stays idle instead of failing against a tokenless backend.
  expect(vi.mocked(usePanAccount).mock.calls.at(-1)?.[0]).toBe(false)
})
