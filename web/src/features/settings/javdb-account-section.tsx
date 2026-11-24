import { useState } from 'react'
import { KeyRoundIcon, LoaderCircleIcon, LogOutIcon } from 'lucide-react'
import { toast } from 'sonner'

import { describeApiError, isJavDBUnauthorized } from '@/api/client'
import { useJavDBAccount, useJavDBLogin, useJavDBLogout } from '@/api/javdb-account'
import { InlineError } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { SettingRow, SettingsSection } from './shared'

export function JavDBAccountSection() {
  const account = useJavDBAccount()
  const login = useJavDBLogin()
  const logout = useJavDBLogout()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')

  const loggedIn = account.data?.logged_in === true
  const disabled = account.isLoading || login.isPending || logout.isPending

  function handleLogin() {
    const trimmed = username.trim()
    if (!trimmed || !password) {
      toast.error('请输入 JavDB 用户名和密码')
      return
    }
    login.mutate(
      { username: trimmed, password },
      {
        onSuccess: () => {
          setPassword('')
          toast.success('JavDB 登录成功')
        },
        onError: error => {
          toast.error(loginErrorMessage(error))
        }
      }
    )
  }

  return (
    <SettingsSection icon={<KeyRoundIcon className="size-4" />} title="JavDB 账号">
      {account.isPending ? (
        <p className="text-sm text-muted-foreground">正在读取登录状态…</p>
      ) : loggedIn ? (
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0 space-y-1">
            <div className="text-sm font-medium">{account.data?.username}</div>
            <div className="text-xs text-muted-foreground">
              已登录 JavDB，收藏与观看记录将使用此账号
            </div>
          </div>
          <Button
            type="button"
            variant="outline"
            disabled={logout.isPending}
            onClick={() =>
              logout.mutate(undefined, {
                onSuccess: () => toast.success('已退出 JavDB 登录'),
                onError: error => toast.error(describeApiError(error))
              })
            }
          >
            {logout.isPending ? (
              <LoaderCircleIcon className="size-4 animate-spin" />
            ) : (
              <LogOutIcon className="size-4" />
            )}
            退出登录
          </Button>
        </div>
      ) : (
        <>
          <SettingRow title="用户名" description="用于登录 JavDB 的邮箱或用户名">
            <Input
              autoComplete="username"
              value={username}
              placeholder="JavDB 用户名"
              disabled={disabled}
              onChange={event => setUsername(event.target.value)}
            />
          </SettingRow>
          <SettingRow title="密码" description="仅保存在本地数据库，用于自动续期登录">
            <Input
              type="password"
              autoComplete="current-password"
              value={password}
              placeholder="JavDB 密码"
              disabled={disabled}
              onChange={event => setPassword(event.target.value)}
              onKeyDown={event => {
                if (event.key === 'Enter') handleLogin()
              }}
            />
          </SettingRow>
          <div className="flex justify-end">
            <Button type="button" disabled={disabled} onClick={handleLogin}>
              {login.isPending ? <LoaderCircleIcon className="size-4 animate-spin" /> : null}
              登录
            </Button>
          </div>
        </>
      )}
      {account.isError ? (
        <InlineError onRetry={() => void account.refetch()} retrying={account.isFetching}>
          暂时无法读取 JavDB 登录状态，请检查后端服务和网络后重试。
        </InlineError>
      ) : null}
      {logout.isError ? <InlineError>{logoutErrorMessage(logout.error)}</InlineError> : null}
    </SettingsSection>
  )
}

function loginErrorMessage(error: unknown) {
  return isJavDBUnauthorized(error) ? describeApiError(error) : 'JavDB 用户名或密码错误'
}

function logoutErrorMessage(error: unknown) {
  return isJavDBUnauthorized(error)
    ? describeApiError(error)
    : '退出登录未完成，请检查后端服务后重试。'
}
