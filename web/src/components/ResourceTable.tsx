// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

import { ArrowDown, ArrowUp, Download } from 'lucide-react';
import { ReactNode, useMemo, useState } from 'react';

export interface Column<T> {
  header: string;
  render: (item: T) => ReactNode;
  // Optional: makes the header click-to-sort. Return a string or number.
  sortValue?: (item: T) => string | number;
  // Optional plain-text form for CSV export; defaults to sortValue.
  csv?: (item: T) => string | number;
}

export function toCsv<T>(items: T[], columns: Column<T>[]): string {
  const cols = columns.filter((c) => c.csv || c.sortValue);
  const esc = (v: string | number) => {
    const s = String(v ?? '');
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
  };
  const rows = items.map((i) => cols.map((c) => esc((c.csv ?? c.sortValue!)(i))).join(','));
  return [cols.map((c) => esc(c.header)).join(','), ...rows].join('\n');
}

// Shared list-table renderer: sortable headers, optional row selection
// (drives the Inspector) and CSV export. Pages that render extra controls
// per row (Machines, Migrations, Snapshots) pass their own action cell as
// a column.
export default function ResourceTable<T>({
  items,
  columns,
  keyFn,
  emptyText,
  onRowClick,
  selectedKey,
  csvName,
}: {
  items: T[];
  columns: Column<T>[];
  keyFn: (item: T) => string;
  emptyText: string;
  onRowClick?: (item: T) => void;
  selectedKey?: string;
  csvName?: string;
}) {
  const [sort, setSort] = useState<{ col: number; dir: 1 | -1 } | null>(null);
  const rows = useMemo(() => {
    if (!sort) return items;
    const sv = columns[sort.col]?.sortValue;
    if (!sv) return items;
    return [...items].sort((a, b) => {
      const x = sv(a);
      const y = sv(b);
      return (typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y))) * sort.dir;
    });
  }, [items, sort, columns]);

  function download() {
    const blob = new Blob([toCsv(rows, columns)], { type: 'text/csv' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = (csvName || 'kairon-export') + '.csv';
    a.click();
    URL.revokeObjectURL(a.href);
  }

  const canExport = !!csvName && items.length > 0;
  return (
    <div className="table-wrap">
      {canExport && (
        <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '8px 10px 0' }}>
          <button className="sm ghost" onClick={download}>
            <Download size={13} /> CSV
          </button>
        </div>
      )}
      <table className="datatable">
        <thead>
          <tr>
            {columns.map((c, i) => (
              <th
                key={c.header}
                className={c.sortValue ? 'sortable' : undefined}
                aria-sort={sort?.col === i ? (sort.dir === 1 ? 'ascending' : 'descending') : undefined}
                onClick={c.sortValue ? () => setSort((s) => (s?.col === i ? { col: i, dir: (s.dir * -1) as 1 | -1 } : { col: i, dir: 1 })) : undefined}
              >
                {c.header}
                {sort?.col === i && (sort.dir === 1 ? <ArrowUp size={11} /> : <ArrowDown size={11} />)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((item) => {
            const k = keyFn(item);
            return (
              <tr
                key={k}
                className={(onRowClick ? 'clickable ' : '') + (selectedKey === k ? 'selected' : '')}
                onClick={onRowClick ? () => onRowClick(item) : undefined}
              >
                {columns.map((c) => (
                  <td key={c.header}>{c.render(item)}</td>
                ))}
              </tr>
            );
          })}
          {rows.length === 0 && (
            <tr>
              <td colSpan={columns.length} className="msg" style={{ textAlign: 'center', padding: 32 }}>
                {emptyText}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
