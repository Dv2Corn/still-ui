import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { describe, expect, it } from 'vitest';

import {
  LOCAL_DIRECT_COUNTRIES,
  localDirectCountryOptions,
} from '@/pages/settings/localDirectCountries';

function backendCountries(): string[] {
  const source = readFileSync(
    resolve(process.cwd(), '..', 'internal', 'sub', 'localdirect', 'countries.go'),
    'utf8',
  );
  const start = source.indexOf('const isoCountries =');
  const block = source.slice(start, source.indexOf('\n\n', start));
  return [...block.matchAll(/"([a-z ]+)"/g)].flatMap((match) => match[1].trim().split(' '));
}

describe('local direct countries', () => {
  it('offers exactly the codes the backend accepts', () => {
    const backend = backendCountries();
    expect(backend).toHaveLength(249);
    expect(LOCAL_DIRECT_COUNTRIES).toEqual(backend);
  });

  it('puts Off first and labels a country with its localized name and code', () => {
    const options = localDirectCountryOptions('Off', 'en-US');
    expect(options[0]).toEqual({ value: '', label: 'Off' });
    expect(options).toContainEqual({ value: 'ru', label: 'Russia (RU)' });
    expect(localDirectCountryOptions('Выключено', 'ru-RU')).toContainEqual({
      value: 'ir',
      label: 'Иран (IR)',
    });
  });
});
