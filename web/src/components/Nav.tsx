import { Activity, Box, ArrowLeftRight, Camera } from 'lucide-react';

export type Page = 'overview' | 'machines' | 'migrations' | 'snapshots';

const ITEMS: [Page, React.ReactNode, string][] = [
  ['overview', <Activity size={17} key="i" />, 'Overview'],
  ['machines', <Box size={17} key="i" />, 'Machines'],
  ['migrations', <ArrowLeftRight size={17} key="i" />, 'Migrations'],
  ['snapshots', <Camera size={17} key="i" />, 'Snapshots'],
];

export default function Nav({ page, setPage }: { page: Page; setPage: (p: Page) => void }) {
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
      <div className="navlinks">
        {ITEMS.map(([id, icon, label]) => (
          <button key={id} className={page === id ? 'active' : ''} onClick={() => setPage(id)}>
            {icon}
            {label}
          </button>
        ))}
      </div>
    </nav>
  );
}
