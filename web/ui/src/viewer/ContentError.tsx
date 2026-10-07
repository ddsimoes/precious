import { useTranslation } from 'react-i18next'

import type { EntryRow } from '@/api/entries'
import { ErrorBanner } from '@/app/ErrorBanner'

// ContentError tells why a file's content cannot be shown. The server refuses
// both a file that changed since the last scan and one it could not read
// with invalid_entry_state; the entry's states tell which.
export function ContentError({ entry, error, onRetry }: { entry: EntryRow; error: unknown; onRetry: () => void }) {
  const { t } = useTranslation()
  const unreadable = entry.content_state === 'unreadable' || entry.state === 'unreadable'
  return (
    <ErrorBanner
      error={error}
      overrides={{
        invalid_entry_state: unreadable ? t('detail.preview.unreadable') : t('detail.preview.changed'),
        source_offline: t('detail.preview.offline'),
      }}
      onRetry={onRetry}
    />
  )
}
