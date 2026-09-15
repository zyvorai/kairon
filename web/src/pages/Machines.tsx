import { useEffect, useState } from 'react';
import { api, apiJSON, getConfig, isAdmin } from '../api';
import { Machine } from '../types';
import { badgeClass } from '../lib/phase';
import Console from './Console';
import Exec from './Exec';

// QEMU is the only backend FluxVM gives a VNC display to at all (Cloud
// Hypervisor/Firecracker have no display device) -- an empty/"auto"
// backend defaults to qemu server-side, so both count as eligible too.
function consoleEligible(m: Machine): boolean {
  const backend = m.spec.runtime?.backend;
  return m.status?.phase === 'Running' && (!backend || backend === 'qemu' || backend === 'auto');
}

// execEligible mirrors internal/uiapi/exec.go's own server-side checks
// that don't depend on the caller's identity (Running + guestAgent
// enabled) -- the admin-only check is applied separately via isAdmin(),
// since that's a property of who's looking at this page, not of the
// Machine. Unlike the VNC console, exec works for every backend: it rides
// FluxVM's real qemu-guest-agent channel, not a QEMU-specific display
// device.
function execEligible(m: Machine): boolean {
  return m.status?.phase === 'Running' && !!m.spec.guestAgent?.enabled;
}

interface CreateForm {
  name: string;
  image: string;
  cpu: string;
  memory: string;
  backend: string;
  network: string;
  netns: boolean;
  hostname: string;
  sshAuthorizedKey: string;
  forward: string;
}

const EMPTY_FORM: CreateForm = {
  name: '',
  image: '',
  cpu: '2',
  memory: '2Gi',
  backend: 'qemu',
  network: 'user',
  netns: false,
  hostname: '',
  sshAuthorizedKey: '',
  forward: '',
};

export default function Machines({ onMigrate, onSnapshot }: { onMigrate: (machine: string) => void; onSnapshot: (machine: string) => void }) {
  const [items, setItems] = useState<Machine[]>([]);
  const [form, setForm] = useState<CreateForm>(EMPTY_FORM);
  const [msg, setMsg] = useState('');
  const [busy, setBusy] = useState(false);
  const [consoleFor, setConsoleFor] = useState<string | null>(null);
  const [execFor, setExecFor] = useState<string | null>(null);
  // consoleEnabled also gates exec: both ride the exact same kairon-ui ->
  // kairon-node relay (internal/consoleproxy), so a deployment either has
  // that relay configured or it doesn't -- see internal/uiapi/exec.go's
  // own doc comment.
  const [consoleEnabled, setConsoleEnabled] = useState(false);

  const refresh = () =>
    api<Machine[]>('/api/v1/machines').then(setItems).catch((e) => setMsg(String(e)));

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 5000);
    getConfig()
      .then((cfg) => setConsoleEnabled(cfg.consoleEnabled))
      .catch(() => setConsoleEnabled(false));
    return () => clearInterval(t);
  }, []);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMsg('');
    try {
      const { hostname, sshAuthorizedKey, forward, ...base } = form;
      const body: Record<string, unknown> = { ...base };
      if (hostname) body.hostname = hostname;
      if (sshAuthorizedKey) body.sshAuthorizedKeys = [sshAuthorizedKey];
      if (forward) {
        const [hostPort, guestPort] = forward.split(':').map((p) => Number(p.trim()));
        if (!hostPort || !guestPort) throw new Error('Port forward must be hostPort:guestPort, e.g. 2222:22');
        body.forwards = [{ hostPort, guestPort }];
      }
      await apiJSON('/api/v1/machines', 'POST', body);
      setForm(EMPTY_FORM);
      await refresh();
    } catch (err) {
      setMsg(String(err));
    } finally {
      setBusy(false);
    }
  }

  async function power(name: string, action: 'start' | 'stop') {
    setMsg('');
    try {
      await api(`/api/v1/machines/default/${encodeURIComponent(name)}/${action}`, { method: 'POST' });
      await refresh();
    } catch (err) {
      setMsg(String(err));
    }
  }

  async function remove(name: string) {
    if (!confirm(`Delete machine "${name}"? This deletes the underlying VM runtime too.`)) return;
    setMsg('');
    try {
      await api(`/api/v1/machines/default/${encodeURIComponent(name)}`, { method: 'DELETE' });
      await refresh();
    } catch (err) {
      setMsg(String(err));
    }
  }

  return (
    <div className="grid">
      <div className="card span4">
        <span className="eyebrow">CREATE MACHINE</span>
        <h3>New machine</h3>
        <form onSubmit={create}>
          <div className="formgrid">
            <label>
              Name
              <input required value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </label>
            <label>
              Image path
              <input required value={form.image} onChange={(e) => setForm({ ...form, image: e.target.value })} placeholder="/var/lib/fluxvm/images/db.qcow2" />
            </label>
            <label>
              CPU
              <input value={form.cpu} onChange={(e) => setForm({ ...form, cpu: e.target.value })} />
            </label>
            <label>
              Memory
              <input value={form.memory} onChange={(e) => setForm({ ...form, memory: e.target.value })} />
            </label>
            <label>
              Backend
              <select value={form.backend} onChange={(e) => setForm({ ...form, backend: e.target.value })}>
                <option value="qemu">qemu</option>
                <option value="cloud-hypervisor">cloud-hypervisor</option>
                <option value="firecracker">firecracker</option>
                <option value="flux-vm">flux-vm</option>
                <option value="auto">auto</option>
              </select>
            </label>
            <label>
              Network
              <select value={form.network} onChange={(e) => setForm({ ...form, network: e.target.value })}>
                <option value="user">user</option>
                <option value="tap">tap</option>
                <option value="macvtap">macvtap</option>
              </select>
            </label>
            <div className="check">
              <input type="checkbox" id="netns" checked={form.netns} onChange={(e) => setForm({ ...form, netns: e.target.checked })} />
              <label htmlFor="netns">Per-VM network namespace</label>
            </div>
            <label>
              Port forward (host:guest)
              <input value={form.forward} onChange={(e) => setForm({ ...form, forward: e.target.value })} placeholder="2222:22 (network=user only)" />
            </label>
            <label>
              Hostname
              <input value={form.hostname} onChange={(e) => setForm({ ...form, hostname: e.target.value })} placeholder="set via cloud-init" />
            </label>
            <label>
              SSH public key
              <input value={form.sshAuthorizedKey} onChange={(e) => setForm({ ...form, sshAuthorizedKey: e.target.value })} placeholder="ssh-ed25519 AAAA... (authorized via cloud-init)" />
            </label>
          </div>
          <div className="formactions">
            <button className="primary" type="submit" disabled={busy}>
              {busy ? 'Creating...' : 'Create machine'}
            </button>
            {msg && <span className={msg.startsWith('Error') ? 'msg error' : 'msg'}>{msg}</span>}
          </div>
        </form>
      </div>

      <div className="card span4">
        <span className="eyebrow">MACHINES</span>
        <table className="datatable">
          <thead>
            <tr>
              <th>Name</th>
              <th>Node</th>
              <th>Phase</th>
              <th>CPU</th>
              <th>Memory</th>
              <th>IP</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {items.map((m) => (
              <tr key={m.metadata.name}>
                <td>{m.metadata.name}</td>
                <td>{m.spec.nodeName || m.status?.nodeName || '-'}</td>
                <td>
                  <span className={badgeClass(m.status?.phase || '')}>{m.status?.phase || 'Unknown'}</span>
                </td>
                <td>{m.spec.resources.cpu}</td>
                <td>{m.spec.resources.memory}</td>
                <td>{m.status?.guestIP || '-'}</td>
                <td>
                  <div className="rowactions">
                    <button onClick={() => power(m.metadata.name, 'start')}>Start</button>
                    <button onClick={() => power(m.metadata.name, 'stop')}>Stop</button>
                    {consoleEnabled && consoleEligible(m) && <button onClick={() => setConsoleFor(m.metadata.name)}>Console</button>}
                    {consoleEnabled && isAdmin() && execEligible(m) && <button onClick={() => setExecFor(m.metadata.name)}>Exec</button>}
                    <button onClick={() => onMigrate(m.metadata.name)}>Migrate</button>
                    <button onClick={() => onSnapshot(m.metadata.name)}>Snapshot</button>
                    <button className="danger" onClick={() => remove(m.metadata.name)}>Delete</button>
                  </div>
                </td>
              </tr>
            ))}
            {items.length === 0 && (
              <tr>
                <td colSpan={7} className="msg">
                  No machines yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {consoleFor && <Console namespace="default" name={consoleFor} onClose={() => setConsoleFor(null)} />}
      {execFor && <Exec namespace="default" name={execFor} onClose={() => setExecFor(null)} />}
    </div>
  );
}
