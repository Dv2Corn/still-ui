import i18next from 'i18next';
import type { InterpolationOptions, Interpolator } from 'i18next';
import { initReactI18next } from 'react-i18next';

import { LanguageManager } from '@/utils';
import type { LanguageScope } from '@/utils';
import { loadAntdLocale } from './AntdRootConfig';
import enUS from '../../../internal/web/translation/en-US.json';

const FALLBACK = 'en-US';

export const INTERPOLATION: InterpolationOptions = {
  escapeValue: false,
  prefix: '{',
  suffix: '}',
};

// No locale uses $t(...) nesting, i18next has no off switch, and its scan is quadratic on
// untrusted text full of "$t(" (node names, remarks), so the nesting regexp never matches.
function disableNesting(interpolator: Interpolator) {
  const target = interpolator as Interpolator & { nestingRegexp: RegExp };
  const resetRegExp = target.resetRegExp.bind(target);
  target.resetRegExp = () => {
    resetRegExp();
    target.nestingRegexp = /(?!)/g;
    return undefined;
  };
  target.resetRegExp();
}

const lazyModules = import.meta.glob([
  '../../../internal/web/translation/*.json',
  '!../../../internal/web/translation/en-US.json',
]);

function moduleKeyFor(code: string): string {
  return `../../../internal/web/translation/${code}.json`;
}

export async function readyI18n(scope: LanguageScope = 'panel') {
  let active = LanguageManager.getLanguage(scope);
  if (
    active !== FALLBACK &&
    !Object.prototype.hasOwnProperty.call(lazyModules, moduleKeyFor(active))
  ) {
    active = FALLBACK;
  }

  await i18next.use(initReactI18next).init({
    lng: active,
    fallbackLng: FALLBACK,
    resources: { [FALLBACK]: { translation: enUS } },
    interpolation: INTERPOLATION,
    returnNull: false,
  });
  disableNesting(i18next.services.interpolator);
  if (active !== FALLBACK) {
    const loader = lazyModules[moduleKeyFor(active)] as
      | (() => Promise<{ default: Record<string, unknown> }>)
      | undefined;
    if (loader) {
      const mod = await loader();
      const messages = (mod.default ?? mod) as Record<string, unknown>;
      i18next.addResourceBundle(active, 'translation', messages, true, true);
      await i18next.changeLanguage(active);
    }
  }
  // The html files ship lang="en"; screen readers pick their voice from it (s-ui#1293).
  document.documentElement.lang = i18next.language;
  await loadAntdLocale(i18next.language);
  return i18next;
}

export { i18next as i18n };
