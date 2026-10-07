import { useTranslation } from 'react-i18next'
import { Link, type To } from 'react-router'

import type { PathStep } from '@/map/pathSteps'

// MapPath is the Map's folder path: a link per step, and the open folder's
// step last.
export function MapPath({ steps, folderLink }: { steps: PathStep[]; folderLink: (step: { id: string }) => To }) {
  const { t } = useTranslation()
  return (
    <nav aria-label={t('map.path')} className="text-sm">
      <ol className="flex flex-wrap items-center gap-1 text-muted-foreground">
        {steps.map((step) =>
          step.current ? (
            <li key={step.id} aria-current="page" className="font-medium break-all text-foreground">
              {step.name}
            </li>
          ) : (
            <li key={step.id} className="flex items-center gap-1">
              <Link to={folderLink(step)} className="break-all text-primary hover:underline">
                {step.name}
              </Link>
              <span aria-hidden="true">/</span>
            </li>
          ),
        )}
      </ol>
    </nav>
  )
}
