import { ReactNode } from 'react';

export interface Column<T> {
  header: string;
  render: (item: T) => ReactNode;
}

// Shared list-table renderer for the read-only resource pages (Quotas,
// DisruptionBudgets, MachineSets, InstanceTypes, MigrationPolicies) --
// pulled out once these five near-identical tables landed together, rather
// than hand-copying the same <table className="datatable"> markup five
// times. Machines/Migrations/Snapshots predate this and stay as they are
// (each also renders a create form inline, not just a table).
export default function ResourceTable<T>({
  items,
  columns,
  keyFn,
  emptyText,
}: {
  items: T[];
  columns: Column<T>[];
  keyFn: (item: T) => string;
  emptyText: string;
}) {
  return (
    <table className="datatable">
      <thead>
        <tr>
          {columns.map((c) => (
            <th key={c.header}>{c.header}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {items.map((item) => (
          <tr key={keyFn(item)}>
            {columns.map((c) => (
              <td key={c.header}>{c.render(item)}</td>
            ))}
          </tr>
        ))}
        {items.length === 0 && (
          <tr>
            <td colSpan={columns.length} className="msg">
              {emptyText}
            </td>
          </tr>
        )}
      </tbody>
    </table>
  );
}
