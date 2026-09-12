import { Activity, ArrowLeftRight, Box, Camera, LogOut } from 'lucide-react';

export type Page = 'overview' | 'machines' | 'migrations' | 'snapshots';

const ITEMS: [Page, React.ReactNode, string][] = [
  ['overview', <Activity size={17} key="i" />, 'Overview'],
  ['machines', <Box size={17} key="i" />, 'Machines'],
  ['migrations', <ArrowLeftRight size={17} key="i" />, 'Migrations'],
  ['snapshots', <Camera size={17} key="i" />, 'Snapshots'],
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
          {username && <span className="navusername">{username}</span>}
          <button onClick={onSignOut}>
            <LogOut size={17} />
            Sign out
          </button>
        </div>
      </div>
    </nav>
  );
}
