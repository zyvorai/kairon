import { useEffect, useState } from 'react';
import { api } from '../api';
import { Overview as OverviewData } from '../types';

export default function Overview() {
  const [data, setData] = useState<OverviewData | null>(null);
  const [msg, setMsg] = useState('');

  useEffect(() => {
    let cancelled = false;
    const refresh = () =>
      api<OverviewData>('/api/v1/overview')
        .then((d) => !cancelled && setData(d))
        .catch((e) => !cancelled && setMsg(String(e)));
    refresh();
    const t = setInterval(refresh, 5000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

  if (msg && !data) return <p className="msg error">{msg}</p>;
  if (!data) return <p className="msg">Loading...</p>;

  return (
    <div className="grid">
      <div className="card">
        <span className="eyebrow">MACHINES</span>
        <h2>{data.machines.total}</h2>
        <div className="grid" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(90px, 1fr))' }}>
          {Object.entries(data.machines.byPhase).map(([phase, count]) => (
            <div className="tile" key={phase}>
              <b>{count}</b>
              <span>{phase}</span>
            </div>
          ))}
        </div>
      </div>

      <div className="card">
        <span className="eyebrow">MIGRATIONS</span>
        <h2>{data.migrations.total}</h2>
        <div className="grid" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(90px, 1fr))' }}>
          <div className="tile">
            <b>{data.migrations.active}</b>
            <span>Active</span>
          </div>
          {Object.entries(data.migrations.byPhase).map(([phase, count]) => (
            <div className="tile" key={phase}>
              <b>{count}</b>
              <span>{phase}</span>
            </div>
          ))}
        </div>
      </div>

      <div className="card span2">
        <span className="eyebrow">FLEET</span>
        <h2>{data.nodes} node{data.nodes === 1 ? '' : 's'}</h2>
        {data.migrations.needsRecovery > 0 ? (
          <div className="tile warning-tile" style={{ marginTop: 12 }}>
            <b>{data.migrations.needsRecovery}</b>
            <span>migration{data.migrations.needsRecovery === 1 ? '' : 's'} NeedsRecovery -- stopped by design, waiting on an operator. See the Migrations page.</span>
          </div>
        ) : (
          <p>No migrations are waiting on operator recovery.</p>
        )}
      </div>
    </div>
  );
}
