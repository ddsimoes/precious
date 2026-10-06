import '@/index.css'
import '@/i18n'

import { QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, RouterProvider } from 'react-router'

import { createQueryClient } from '@/app/queryClient'
import { routes } from '@/app/routes'

const root = document.getElementById('root')
if (root === null) {
  throw new Error('index.html has no #root element')
}

const queryClient = createQueryClient()
const router = createBrowserRouter(routes)

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
