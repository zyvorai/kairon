import { useEffect, useState } from 'react';
import { api } from '../api';
import { MachineSet } from '../types';
import ResourceTable from '../components/ResourceTable';
import { badgeClass } from '../lib/phase';

export default function MachineSets() {
  const [items, setItems] = useState<MachineSet[]>([]);
  const [msg, setMsg] = useState('');

  const refresh = () => api<MachineSet[]>('/api/v1/machinesets').then(setItems).catch((e) => setMsg(String(e)));

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 5000);
    return () => clearInterval(t);
  }, []);

  return (
    <div className="grid">
      <div className="card span4">
        <span className="eyebrow">MACHINE SETS</span>
        {msg && <p className="msg error">{msg}</p>}
        <ResourceTable
          items={items}
          keyFn={(s) => s.metadata.name}
          emptyText="No MachineSets yet."
          columns={[
            { header: 'Name', render: (s) => s.metadata.name },
            { header: 'Replicas', render: (s) => s.spec.replicas },
            {
              header: 'Ready',
              render: (s) => {
                const ready = s.status?.readyReplicas ?? 0;
                const desired = s.status?.replicas ?? s.spec.replicas;
                return (
                  <span className={badgeClass(ready >= desired && desired > 0 ? 'Running' : 'Pending')}>
                    {ready}/{desired}
                  </span>
                );
              },
            },
            { header: 'Updated', render: (s) => s.status?.updatedReplicas ?? 0 },
            { header: 'Strategy', render: (s) => s.spec.strategy || 'RollingUpdate' },
            { header: 'Message', render: (s) => s.status?.message || '-' },
          ]}
        />
      </div>
    </div>
  );
}
