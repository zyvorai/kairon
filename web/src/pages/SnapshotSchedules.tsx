import { useEffect, useState } from 'react';
import { api, apiJSON } from '../api';
import { MachineSnapshotSchedule } from '../types';
import ResourceTable from '../components/ResourceTable';
import { badgeClass } from '../lib/phase';

function formatSelector(selector: Record<string, string>): string {
  const entries = Object.entries(selector || {});
  if (entries.length === 0) return '-';
  return entries.map(([k, v]) => `${k}=${v}`).join(', ');
}

export default function SnapshotSchedules() {
  const [items, setItems] = useState<MachineSnapshotSchedule[]>([]);
  const [msg, setMsg] = useState('');

  const refresh = () =>
    api<MachineSnapshotSchedule[]>('/api/v1/snapshot-schedules').then(setItems).catch((e) => setMsg(String(e)));

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 5000);
    return () => clearInterval(t);
  }, []);

  const toggleSuspend = async (s: MachineSnapshotSchedule) => {
    try {
      await apiJSON(`/api/v1/snapshot-schedules/default/${encodeURIComponent(s.metadata.name)}/suspend`, 'PATCH', {
        suspend: !s.spec.suspend,
      });
      refresh();
    } catch (e) {
      setMsg(String(e));
    }
  };

  return (
    <div className="grid">
      <div className="card span4">
        <span className="eyebrow">SNAPSHOT SCHEDULES</span>
        {msg && <p className="msg error">{msg}</p>}
        <ResourceTable
          items={items}
          keyFn={(s) => s.metadata.name}
          emptyText="No MachineSnapshotSchedules yet."
          columns={[
            { header: 'Name', render: (s) => s.metadata.name },
            { header: 'Selector', render: (s) => formatSelector(s.spec.selector) },
            { header: 'Interval', render: (s) => `${s.spec.intervalSeconds}s` },
            { header: 'Keep last', render: (s) => (s.spec.keepLast ? s.spec.keepLast : 'unbounded') },
            {
              header: 'Status',
              render: (s) => (
                <span className={badgeClass(s.spec.suspend ? 'Paused' : 'Running')}>
                  {s.spec.suspend ? 'Suspended' : 'Active'}
                </span>
              ),
            },
            { header: 'Last run', render: (s) => (s.status?.lastRunTime ? new Date(s.status.lastRunTime).toLocaleString() : 'never') },
            { header: 'Last count', render: (s) => s.status?.lastRunSnapshotCount ?? 0 },
            { header: 'Last error', render: (s) => s.status?.lastRunError || '-' },
            {
              header: '',
              render: (s) => (
                <button onClick={() => toggleSuspend(s)}>{s.spec.suspend ? 'Resume' : 'Suspend'}</button>
              ),
            },
          ]}
        />
      </div>
    </div>
  );
}
