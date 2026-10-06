import { useTranslation } from 'react-i18next'

import { PageTitle } from '@/app/PageTitle'

export function NotFoundPage() {
  const { t } = useTranslation()
  return <PageTitle>{t('pages.notFound')}</PageTitle>
}
