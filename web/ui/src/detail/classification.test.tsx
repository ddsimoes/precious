import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { Classification, EntryDetail } from '@/api/entries'
import { entryDetail, entryRow, folderRow, fotosSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

// siteAntigo is a folder the rules classify as a software project, which
// makes it a group; owner overrides what the owner set.
function siteAntigo(owner: Classification['owner'] = { category: null, group: null }): EntryDetail {
  const category = owner.category ?? 'source_project'
  const group = owner.group ?? (owner.category === null ? true : false)
  const row = folderRow('30', 'site_antigo', {
    category,
    family: 'personal',
    triage: 'keep',
    group,
  })
  return entryDetail(row, {
    classification: {
      category,
      family: 'personal',
      traits: [],
      triage: 'keep',
      group,
      veto: false,
      rules: [{ id: 'source_project', explain: 'It holds version control or a build file.' }],
      indicators: [],
      owner,
      rules_category: 'source_project',
      rules_group: true,
    },
  })
}

function routes(detail: () => EntryDetail, extra: Record<string, (request: Request) => Response | Promise<Response>> = {}) {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    'GET /api/entries/30': () => jsonResponse(200, detail()),
    ...extra,
  }
}

function commandBodies(requests: Request[], name: string) {
  return Promise.all(
    requests
      .filter((r) => r.method === 'POST' && new URL(r.url).pathname === `/api/commands/${name}`)
      .map((r) => r.clone().json() as Promise<unknown>),
  )
}

const scanStarted = { applied: 1, scan: { job_id: '77', coalesced: false } }

describe('Classification controls', () => {
  it('shows what the owner set beside what the rules say', async () => {
    stubApi(routes(() => siteAntigo({ category: 'documents', group: true })))
    renderApp('/map/1?entry=30')

    const region = within(await screen.findByRole('region', { name: 'Classification' }))
    expect(region.getByText('Documents', { selector: 'dd' })).toHaveTextContent('Documents set by you')
    expect(region.getByText('The rules say: Software project')).toBeInTheDocument()
    expect(region.getByText(/^Treated as a whole/)).toHaveTextContent('set by you')
    expect(region.getByText('The rules say: treated as a whole')).toBeInTheDocument()
    const select = region.getByRole('combobox', { name: 'Change category' })
    expect(select).toHaveValue('documents')
    expect(within(select).getByRole('option', { name: 'Back to the rules (Software project)' })).toHaveValue('rules')
    expect(region.getByRole('button', { name: 'Yes' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('shows no owner label while the rules decide', async () => {
    stubApi(routes(() => siteAntigo()))
    renderApp('/map/1?entry=30')

    const region = within(await screen.findByRole('region', { name: 'Classification' }))
    expect(region.getByRole('combobox', { name: 'Change category' })).toHaveValue('rules')
    expect(region.getByRole('option', { name: 'As the rules say (Software project)' })).toBeInTheDocument()
    expect(region.queryByText('set by you')).not.toBeInTheDocument()
    expect(region.queryByText(/The rules say/)).not.toBeInTheDocument()
    expect(region.getByRole('button', { name: 'As the rules say (Yes)' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('sets the category and gives it back to the rules, refreshing the entry', async () => {
    let detail = siteAntigo()
    const requests = stubApi(
      routes(() => detail, {
        'POST /api/commands/set-category': async (request) => {
          const body = (await request.clone().json()) as { category: string }
          detail = body.category === 'rules' ? siteAntigo() : siteAntigo({ category: 'documents', group: null })
          return jsonResponse(200, scanStarted)
        },
      }),
    )
    renderApp('/map/1?entry=30')
    const region = within(await screen.findByRole('region', { name: 'Classification' }))

    await userEvent.selectOptions(region.getByRole('combobox', { name: 'Change category' }), 'documents')
    await waitFor(() => expect(region.getByText('The rules say: Software project')).toBeInTheDocument())
    expect(region.getByRole('status')).toHaveTextContent(
      'Saved. The figures of the folders above update when the scan of this disk ends.',
    )
    expect(region.getByRole('combobox', { name: 'Change category' })).toHaveValue('documents')

    await userEvent.selectOptions(region.getByRole('combobox', { name: 'Change category' }), 'rules')
    await waitFor(() => expect(region.queryByText('The rules say: Software project')).not.toBeInTheDocument())

    expect(await commandBodies(requests, 'set-category')).toEqual([
      { entry_id: '30', category: 'documents' },
      { entry_id: '30', category: 'rules' },
    ])
    expect(requests.filter((r) => r.method === 'GET' && new URL(r.url).pathname === '/api/entries/30')).toHaveLength(3)
  })

  it('marks, unmarks, and returns the group to the rules with three states', async () => {
    let detail = siteAntigo()
    const requests = stubApi(
      routes(() => detail, {
        'POST /api/commands/set-group': async (request) => {
          const body = (await request.clone().json()) as { group: boolean | 'rules' }
          detail = siteAntigo({ category: null, group: body.group === 'rules' ? null : body.group })
          return jsonResponse(200, { applied: 1, scan: null })
        },
      }),
    )
    renderApp('/map/1?entry=30')
    const region = within(await screen.findByRole('region', { name: 'Classification' }))
    const group = within(region.getByRole('group', { name: 'Review as one item' }))

    await userEvent.click(group.getByRole('button', { name: 'No' }))
    await waitFor(() => expect(group.getByRole('button', { name: 'No' })).toHaveAttribute('aria-pressed', 'true'))
    expect(region.getByText(/^Reviewed item by item/)).toHaveTextContent('set by you')
    expect(region.getByText('The rules say: treated as a whole')).toBeInTheDocument()
    expect(region.getByRole('status')).toHaveTextContent(
      'Saved. This disk is not connected: the figures of the folders above update at its next scan.',
    )

    await userEvent.click(group.getByRole('button', { name: 'Yes' }))
    await waitFor(() => expect(group.getByRole('button', { name: 'Yes' })).toHaveAttribute('aria-pressed', 'true'))
    await userEvent.click(group.getByRole('button', { name: 'As the rules say (Yes)' }))
    await waitFor(() =>
      expect(group.getByRole('button', { name: 'As the rules say (Yes)' })).toHaveAttribute('aria-pressed', 'true'),
    )

    expect(await commandBodies(requests, 'set-group')).toEqual([
      { entry_id: '30', group: false },
      { entry_id: '30', group: true },
      { entry_id: '30', group: 'rules' },
    ])
  })

  it('offers a file the category only, and an archive member nothing', async () => {
    const member = entryRow({ id: 'm5', name: 'emule.exe', archive_id: '40' })
    stubApi({
      ...routes(() => siteAntigo()),
      'GET /api/entries/12': () => jsonResponse(200, entryDetail(entryRow())),
      'GET /api/entries/m5': () => jsonResponse(200, entryDetail(member)),
    })
    const { router } = renderApp('/search?entry=12')

    let region = within(await screen.findByRole('region', { name: 'Classification' }))
    expect(region.getByRole('combobox', { name: 'Change category' })).toBeInTheDocument()
    expect(region.queryByRole('group', { name: 'Review as one item' })).not.toBeInTheDocument()

    await router.navigate('/search?entry=m5')
    await screen.findByRole('complementary', { name: 'emule.exe' })
    region = within(screen.getByRole('region', { name: 'Classification' }))
    expect(region.queryByRole('combobox')).not.toBeInTheDocument()
    expect(region.queryByRole('group', { name: 'Review as one item' })).not.toBeInTheDocument()
  })
})
