import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'

import { en } from '@/i18n/en'

declare module 'i18next' {
  interface CustomTypeOptions {
    defaultNS: 'translation'
    resources: { translation: typeof en }
  }
}

void i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  // The catalog is bundled, so translations are ready before the first render.
  initAsync: false,
  resources: { en: { translation: en } },
  // React escapes rendered text already.
  interpolation: { escapeValue: false },
})
