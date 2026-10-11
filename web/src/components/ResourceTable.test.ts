// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { toCsv } from './ResourceTable';

describe('toCsv', () => {
  it('exports sortable/csv columns and escapes', () => {
    const items = [{ n: 'a,b', c: 2 }, { n: 'say "hi"', c: 3 }];
    const out = toCsv(items, [
      { header: 'Name', render: (i) => i.n, sortValue: (i) => i.n },
      { header: 'CPU', render: (i) => i.c, csv: (i) => i.c },
      { header: 'Actions', render: () => null },
    ]);
    expect(out).toBe('Name,CPU\n"a,b",2\n"say ""hi""",3');
  });
});
