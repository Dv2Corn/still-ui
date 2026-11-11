import { describe, expect, it } from 'vitest';

import { CONNLOG_PAGE_ROWS, EMPTY_CONNLOG_FORM, connEndpoint, connLogFilter } from '@/lib/connlog';

describe('connLogFilter', () => {
  it('sends an untouched form as "no filter", which the API reads as zeros and empty strings', () => {
    expect(connLogFilter(EMPTY_CONNLOG_FORM)).toEqual({
      client: '',
      inbound: '',
      host: '',
      source: '',
      from: 0,
      to: 0,
      before: '',
      limit: CONNLOG_PAGE_ROWS,
    });
  });

  it('trims pasted text and carries the time range and the page cursor', () => {
    const filter = connLogFilter(
      {
        client: ' alice@example.com ',
        inbound: 'inbound-443',
        host: '\texample.com\n',
        source: ' 203.0.113. ',
        from: 1735689600000,
        to: 1735775999999,
      },
      '20250101:4096',
    );
    expect(filter).toMatchObject({
      client: 'alice@example.com',
      host: 'example.com',
      source: '203.0.113.',
      from: 1735689600000,
      to: 1735775999999,
      before: '20250101:4096',
    });
  });
});

describe('connEndpoint', () => {
  it.each([
    ['example.com', 443, 'example.com:443'],
    ['203.0.113.9', 54321, '203.0.113.9:54321'],
    ['2001:db8::1', 443, '[2001:db8::1]:443'],
    ['203.0.113.0/24', 0, '203.0.113.0/24'],
    ['2001:db8:aa::/48', 0, '2001:db8:aa::/48'],
    ['', 0, ''],
  ])('writes %s port %i as %s', (address, port, want) => {
    expect(connEndpoint(address, port)).toBe(want);
  });
});
