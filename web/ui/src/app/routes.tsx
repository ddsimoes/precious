import type { RouteObject } from 'react-router'

import { AppLayout } from '@/app/AppLayout'
import { LoginPage } from '@/app/LoginPage'
import { NotFoundPage } from '@/app/NotFoundPage'
import { CheckReportPage } from '@/cleanup/CheckReport'
import { CleanupPage } from '@/cleanup/CleanupPage'
import { ComparePage } from '@/compare/ComparePage'
import { HistoryPage } from '@/history/HistoryPage'
import { HomePage } from '@/home/HomePage'
import { MapPage } from '@/map/MapPage'
import { OpportunitiesPage } from '@/opportunities/OpportunitiesPage'
import { ReviewListPage } from '@/opportunities/ReviewListPage'
import { SimilarFoldersPage } from '@/opportunities/SimilarFoldersPage'
import { SearchPage } from '@/search/SearchPage'
import { SourcesPage } from '@/sources/SourcesPage'

// routes are the client routes (design D14). The server answers every
// non-/api GET with the app shell, so each of these deep-links.
export const routes: RouteObject[] = [
  { path: '/login', element: <LoginPage /> },
  {
    element: <AppLayout />,
    children: [
      { index: true, element: <HomePage /> },
      // Without an entry id (the Map link in the header) the Map screen picks its start.
      { path: 'map/:entryId?', element: <MapPage /> },
      { path: 'search', element: <SearchPage /> },
      { path: 'opportunities', element: <OpportunitiesPage /> },
      { path: 'opportunities/similar', element: <SimilarFoldersPage /> },
      { path: 'opportunities/:list', element: <ReviewListPage /> },
      // Compare names its two sides in the address: ?left=&right=&bucket=.
      { path: 'compare', element: <ComparePage /> },
      // Cleanup: plans and the quarantine, ?source= limiting them to one
      // source, and the report of each check before deleting for good.
      { path: 'cleanup', element: <CleanupPage /> },
      { path: 'cleanup/checks/:checkId', element: <CheckReportPage /> },
      { path: 'history', element: <HistoryPage /> },
      { path: 'sources', element: <SourcesPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
]
