import type { RouteObject } from 'react-router'

import { AppLayout } from '@/app/AppLayout'
import { LoginPage } from '@/app/LoginPage'
import { NotFoundPage } from '@/app/NotFoundPage'
import { HomePage } from '@/home/HomePage'
import { MapPage } from '@/map/MapPage'
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
      { path: 'sources', element: <SourcesPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
]
