import { useEffect, useState } from 'react';
import Nav, { Page } from './components/Nav';
import Overview from './pages/Overview';
import Machines from './pages/Machines';
import Migrations from './pages/Migrations';
import Snapshots from './pages/Snapshots';
import Restores from './pages/Restores';
import Quotas from './pages/Quotas';
import DisruptionBudgets from './pages/DisruptionBudgets';
import MachineSets from './pages/MachineSets';
import InstanceTypes from './pages/InstanceTypes';
import MigrationPolicies from './pages/MigrationPolicies';
import SnapshotSchedules from './pages/SnapshotSchedules';
import NetworkPolicies from './pages/NetworkPolicies';
import SecurityGroups from './pages/SecurityGroups';
import Nodes from './pages/Nodes';
import Account from './pages/Account';
import Login from './pages/Login';
import OIDCCallback from './pages/OIDCCallback';
import { logout, token, UNAUTHORIZED_EVENT, username } from './api';

export default function App() {
  const [page, setPage] = useState<Page>('overview');
  const [signedIn, setSignedIn] = useState(!!token());
  const [prefillMachine, setPrefillMachine] = useState('');
  const [prefillSnapshot, setPrefillSnapshot] = useState('');
  // No client-side router elsewhere in this app -- this one path is the
  // sole exception, since the OIDC callback (see internal/uiapi/oidc.go)
  // has to land somewhere real rather than at whatever page the operator
  // happened to be on when they clicked "Sign in with SSO". Mutable (not
  // a one-time lazy useState) because onDone() below must be able to turn
  // it back off once handled -- otherwise this stays true for the rest of
  // the page's life (window.location.pathname doesn't get re-read after
  // the initial render just because history.replaceState changed it), and
  // OIDCCallback renders nothing on success, leaving the screen blank
  // forever until a manual reload.
  const [onOIDCCallback, setOnOIDCCallback] = useState(() => window.location.pathname === '/oidc/callback');

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
  function goRestore(snapshot: string) {
    setPrefillSnapshot(snapshot);
    setPage('restores');
  }

  async function signOut() {
    await logout().catch(() => {});
    setSignedIn(false);
  }

  if (onOIDCCallback) {
    return (
      <OIDCCallback
        onDone={() => {
          window.history.replaceState(null, '', '/');
          setSignedIn(true);
          setOnOIDCCallback(false);
        }}
      />
    );
  }

  if (!signedIn) {
    return <Login onSignedIn={() => setSignedIn(true)} />;
  }

  const body = {
    overview: <Overview />,
    machines: <Machines onMigrate={goMigrate} onSnapshot={goSnapshot} />,
    migrations: <Migrations prefillMachine={prefillMachine} />,
    snapshots: <Snapshots prefillMachine={prefillMachine} onRestore={goRestore} />,
    restores: <Restores prefillSnapshot={prefillSnapshot} />,
    quotas: <Quotas />,
    'disruption-budgets': <DisruptionBudgets />,
    machinesets: <MachineSets />,
    instancetypes: <InstanceTypes />,
    'migration-policies': <MigrationPolicies />,
    'snapshot-schedules': <SnapshotSchedules />,
    'network-policies': <NetworkPolicies />,
    'security-groups': <SecurityGroups />,
    nodes: <Nodes />,
    account: <Account />,
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
