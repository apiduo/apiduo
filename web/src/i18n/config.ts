/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import i18n from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'

import { convertDetectedLanguage } from './languages'
import en from './locales/en.json'
import fr from './locales/fr.json'
import ja from './locales/ja.json'
import ru from './locales/ru.json'
import vi from './locales/vi.json'
import zhTW from './locales/zh-TW.json'
import zhCN from './locales/zh.json'

/* APIDUO:i18n:BEGIN */
import apiduoEn from './apiduo/en.json'
import apiduoFr from './apiduo/fr.json'
import apiduoJa from './apiduo/ja.json'
import apiduoRu from './apiduo/ru.json'
import apiduoVi from './apiduo/vi.json'
import apiduoZhTW from './apiduo/zh-TW.json'
import apiduoZhCN from './apiduo/zh.json'
/* APIDUO:i18n:END */

export const resources = {
  en,
  zhCN,
  fr,
  ru,
  ja,
  vi,
  zhTW,
} as const

/* APIDUO:i18n:BEGIN
 * apiduo text lives in its own files. The upstream locale files stay byte
 * identical to upstream, so a rebase never produces a conflict over a phrase.
 * These values win over the upstream map: if someone runs i18n:sync and the
 * same key lands back in a locale file, the apiduo wording still renders.
 */
const apiduoLocales: Record<string, { translation: Record<string, string> }> = {
  en: apiduoEn,
  zhCN: apiduoZhCN,
  fr: apiduoFr,
  ru: apiduoRu,
  ja: apiduoJa,
  vi: apiduoVi,
  zhTW: apiduoZhTW,
}

for (const [code, extra] of Object.entries(apiduoLocales)) {
  const target = (
    resources as Record<string, { translation: Record<string, string> }>
  )[code]
  if (target) {
    Object.assign(target.translation, extra.translation)
  }
}
/* APIDUO:i18n:END */

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources,
    fallbackLng: 'en',
    supportedLngs: ['en', 'zhCN', 'fr', 'ru', 'ja', 'vi', 'zhTW'],
    load: 'currentOnly',
    nsSeparator: false, // Allow literal colons in keys (e.g., URLs, labels)
    debug: import.meta.env.DEV,
    interpolation: {
      escapeValue: false, // not needed for react as it escapes by default
    },
    detection: {
      order: ['localStorage', 'navigator'],
      caches: ['localStorage'],
      // Browsers report `zh-CN`/`zh-TW`/`zh`; map them onto our `zhCN`/`zhTW`
      // codes (non-Chinese codes pass through for normal supportedLngs matching).
      convertDetectedLanguage,
    },
  })

export default i18n
