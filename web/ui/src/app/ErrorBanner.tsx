import { useTranslation } from 'react-i18next'

import { ApiError } from '@/app/api'
import { errorMessage, type MessageOverrides } from '@/app/errors'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

interface ErrorBannerProps {
  error: unknown
  overrides?: MessageOverrides
  onRetry?: () => void
  // retryLabel names the retry button when it does more than try again.
  retryLabel?: string
  onDismiss?: () => void
  className?: string
}

// ErrorBanner shows a failed request inline. The error code and the server's
// message stay in a collapsed technical details section.
export function ErrorBanner({
  error,
  overrides,
  onRetry,
  retryLabel,
  onDismiss,
  className,
}: ErrorBannerProps) {
  const { t } = useTranslation()
  return (
    <div
      role="alert"
      className={cn(
        'grid gap-2 rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm',
        className,
      )}
    >
      <p className="font-medium text-destructive">{errorMessage(t, error, overrides)}</p>
      {error instanceof ApiError && (
        <details className="text-xs text-muted-foreground">
          <summary className="cursor-pointer">{t('errors.details')}</summary>
          <p className="mt-1 font-mono break-all">
            {error.status} {error.code}: {error.message}
          </p>
        </details>
      )}
      {(onRetry !== undefined || onDismiss !== undefined) && (
        <div className="flex gap-2">
          {onRetry !== undefined && (
            <Button variant="outline" size="sm" onClick={onRetry}>
              {retryLabel ?? t('app.retry')}
            </Button>
          )}
          {onDismiss !== undefined && (
            <Button variant="ghost" size="sm" onClick={onDismiss}>
              {t('errors.dismiss')}
            </Button>
          )}
        </div>
      )}
    </div>
  )
}
