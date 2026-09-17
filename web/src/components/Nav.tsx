import { Activity, ArrowLeftRight, Box, Calendar, Camera, Cpu, Gauge, Layers, LogOut, Network, RotateCcw, Route, Server, Shield, ShieldCheck, UserCog } from 'lucide-react';

export type Page =
  | 'overview'
  | 'machines'
  | 'migrations'
  | 'snapshots'
  | 'restores'
  | 'quotas'
  | 'disruption-budgets'
  | 'machinesets'
  | 'instancetypes'
  | 'migration-policies'
  | 'snapshot-schedules'
  | 'network-policies'
  | 'security-groups'
  | 'nodes'
  | 'account';

const ITEMS: [Page, React.ReactNode, string][] = [
  ['overview', <Activity size={17} key="i" />, 'Overview'],
  ['machines', <Box size={17} key="i" />, 'Machines'],
  ['migrations', <ArrowLeftRight size={17} key="i" />, 'Migrations'],
  ['snapshots', <Camera size={17} key="i" />, 'Snapshots'],
  ['restores', <RotateCcw size={17} key="i" />, 'Restores'],
  ['quotas', <Gauge size={17} key="i" />, 'Quotas'],
  ['disruption-budgets', <Shield size={17} key="i" />, 'Disruption budgets'],
  ['machinesets', <Layers size={17} key="i" />, 'Machine sets'],
  ['instancetypes', <Cpu size={17} key="i" />, 'Instance types'],
  ['migration-policies', <Route size={17} key="i" />, 'Migration policies'],
  ['snapshot-schedules', <Calendar size={17} key="i" />, 'Snapshot schedules'],
  ['network-policies', <Network size={17} key="i" />, 'Network policies'],
  ['security-groups', <ShieldCheck size={17} key="i" />, 'Security groups'],
  ['nodes', <Server size={17} key="i" />, 'Nodes'],
];

export default function Nav({
  page,
  setPage,
  username,
  onSignOut,
}: {
  page: Page;
  setPage: (p: Page) => void;
  username?: string;
  onSignOut: () => void;
}) {
  return (
    <nav className="nav">
      <div className="brand">
        <img className="dot" src="/zyvor-favicon.svg" alt="Zyvor" width={20} height={20} />
        KAIRON{' '}
        <small>
          by{' '}
          <a href="https://zyvor.dev" target="_blank" rel="noreferrer">
            Zyvor
          </a>
        </small>
      </div>
      <div className="navright">
        <div className="navlinks">
          {ITEMS.map(([id, icon, label]) => (
            <button key={id} className={page === id ? 'active' : ''} onClick={() => setPage(id)}>
              {icon}
              {label}
            </button>
          ))}
        </div>
        <div className="navuser">
          {username && (
            <button className={`navusername-btn ${page === 'account' ? 'active' : ''}`} onClick={() => setPage('account')}>
              <UserCog size={17} />
              {username}
            </button>
          )}
          <button onClick={onSignOut}>
            <LogOut size={17} />
            Sign out
          </button>
        </div>
      </div>
    </nav>
  );
}
