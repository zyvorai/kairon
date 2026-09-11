import { useState } from 'react';
import Nav, { Page } from './components/Nav';
import Overview from './pages/Overview';
import Machines from './pages/Machines';
import Migrations from './pages/Migrations';
import Snapshots from './pages/Snapshots';
import { setToken, token } from './api';

export default function App() {
  const [page, setPage] = useState<Page>('overview');
  const [tok, setTok] = useState(token());
  const [prefillMachine, setPrefillMachine] = useState('');

  function goMigrate(machine: string) {
    setPrefillMachine(machine);
    setPage('migrations');
  }
  function goSnapshot(machine: string) {
    setPrefillMachine(machine);
    setPage('snapshots');
  }

  const body = {
    overview: <Overview />,
    machines: <Machines onMigrate={goMigrate} onSnapshot={goSnapshot} />,
    migrations: <Migrations prefillMachine={prefillMachine} />,
    snapshots: <Snapshots prefillMachine={prefillMachine} />,
  }[page];

  return (
    <>
      <Nav page={page} setPage={setPage} />
      <main>
        <header className="hero">
          <div>
            <p className="eyebrow">VM ORCHESTRATION · NO KUBEVIRT · NO LIBVIRT</p>
            <h1>Run, migrate, and snapshot VMs on FluxVM.</h1>
            <p>Kairon orchestrates FluxVM hosts as Kubernetes-native Machines, with secure peer live migration and operator-attested recovery.</p>
          </div>
          <label className="tokenbox">
            API token
            <input
              type="password"
              value={tok}
              placeholder="required unless the server allows unauthenticated access"
              onChange={(e) => {
                setTok(e.target.value);
                setToken(e.target.value);
              }}
            />
          </label>
        </header>
        {body}
      </main>
    </>
  );
}
