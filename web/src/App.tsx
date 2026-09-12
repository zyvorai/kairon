import { useEffect, useState } from 'react';
import Nav, { Page } from './components/Nav';
import Overview from './pages/Overview';
import Machines from './pages/Machines';
import Migrations from './pages/Migrations';
import Snapshots from './pages/Snapshots';
import Login from './pages/Login';
import { logout, token, UNAUTHORIZED_EVENT, username } from './api';

export default function App() {
  const [page, setPage] = useState<Page>('overview');
  const [signedIn, setSignedIn] = useState(!!token());
  const [prefillMachine, setPrefillMachine] = useState('');

  useEffect(() => {
    // Fired by api.ts on any 401, from anywhere in the app -- a stale or
    // revoked session bounces straight back to the login screen instead of
    // leaving the dashboard up with every action silently failing.
    const onUnauthorized = () => setSignedIn(false);
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
  }, []);

  function goMigrate(machine: string) {
    setPrefillMachine(machine);
    setPage('migrations');
  }
  function goSnapshot(machine: string) {
    setPrefillMachine(machine);
    setPage('snapshots');
  }

  async function signOut() {
    await logout().catch(() => {});
    setSignedIn(false);
  }

  if (!signedIn) {
    return <Login onSignedIn={() => setSignedIn(true)} />;
  }

  const body = {
    overview: <Overview />,
    machines: <Machines onMigrate={goMigrate} onSnapshot={goSnapshot} />,
    migrations: <Migrations prefillMachine={prefillMachine} />,
    snapshots: <Snapshots prefillMachine={prefillMachine} />,
  }[page];

  return (
    <>
      <Nav page={page} setPage={setPage} username={username()} onSignOut={signOut} />
      <main>
        <header className="hero">
          <div>
            <p className="eyebrow">VM ORCHESTRATION · NO KUBEVIRT · NO LIBVIRT</p>
            <h1>Run, migrate, and snapshot VMs on FluxVM.</h1>
            <p>Kairon orchestrates FluxVM hosts as Kubernetes-native Machines, with secure peer live migration and operator-attested recovery.</p>
          </div>
        </header>
        {body}
      </main>
    </>
  );
}
