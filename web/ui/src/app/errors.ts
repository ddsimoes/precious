import type { TFunction } from 'i18next'

import { ApiError } from '@/app/api'
import { en } from '@/i18n/en'

type KnownCode = keyof typeof en.errors.codes

// MessageOverrides replaces the message of some error codes where a screen
// knows better what they mean there.
export type MessageOverrides = Partial<Record<string, string>>

// errorMessage turns a failed request into a sentence for the owner: the
// error envelope's code picks it, so the server's own text never needs to
// be shown.
export function errorMessage(t: TFunction, error: unknown, overrides: MessageOverrides = {}): string {
  if (error instanceof ApiError) {
    const override = overrides[error.code]
    if (override !== undefined) {
      return override
    }
    if (Object.hasOwn(en.errors.codes, error.code)) {
      return t(`errors.codes.${error.code as KnownCode}`)
    }
    return t('errors.unknown')
  }
  // fetch rejects with a TypeError when the server cannot be reached.
  if (error instanceof TypeError) {
    return t('errors.network')
  }
  return t('errors.unknown')
}
