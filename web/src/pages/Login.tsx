import { useEffect, useState } from 'react';
import { AuthConfig, getAuthConfig, login, setToken } from '../api';

type Step = 'username' | 'password';

function LoginChrome({ children }: { children: React.ReactNode }) {
  return (
    <div className="loginwrap">
      <div className="loginbrand">
        <img className="dot" src="/zyvor-favicon.svg" alt="Zyvor" width={28} height={28} />
        <div>
          <div className="loginbrand-name">
            KAIRON <small>by Zyvor</small>
          </div>
          <p className="loginbrand-tagline">VM orchestration on FluxVM &mdash; no KubeVirt, no libvirt.</p>
        </div>
      </div>
      <div className="card logincard">{children}</div>
      <p className="loginhost">
        Connecting to <code>{window.location.host}</code>
      </p>
    </div>
  );
}

export default function Login({ onSignedIn }: { onSignedIn: () => void }) {
  const [config, setConfig] = useState<AuthConfig | null>(null);
  const [step, setStep] = useState<Step>('username');
  const [user, setUser] = useState('');
  const [password, setPassword] = useState('');
  const [rawToken, setRawToken] = useState('');
  const [msg, setMsg] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    getAuthConfig()
      .then(setConfig)
      // A server old enough to not have /api/v1/auth/config yet, or one
      // that's unreachable, both fall back to the legacy raw-token box.
      .catch(() => setConfig({ loginEnabled: false, tokenEnabled: true }));
  }, []);

  function continueToPassword(e: React.FormEvent) {
    e.preventDefault();
    setMsg('');
    setStep('password');
  }

  async function signIn(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMsg('');
    try {
      await login(user, password);
      onSignedIn();
    } catch (err) {
      setPassword('');
      setMsg(String(err));
    } finally {
      setBusy(false);
    }
  }

  function useToken(e: React.FormEvent) {
    e.preventDefault();
    setToken(rawToken);
    onSignedIn();
  }

  if (!config) {
    return (
      <LoginChrome>
        <div className="loginskeleton" />
      </LoginChrome>
    );
  }

  if (!config.loginEnabled) {
    return (
      <LoginChrome>
        <span className="eyebrow">SIGN IN</span>
        <h3>API token</h3>
        <form onSubmit={useToken}>
          <div className="formgrid">
            <label>
              Token
              <input
                type="password"
                autoFocus
                value={rawToken}
                placeholder="required unless the server allows unauthenticated access"
                onChange={(e) => setRawToken(e.target.value)}
              />
            </label>
          </div>
          <div className="formactions">
            <button className="primary" type="submit">
              Continue
            </button>
          </div>
        </form>
      </LoginChrome>
    );
  }

  return (
    <LoginChrome>
      <span className="eyebrow">SIGN IN</span>
      {step === 'username' ? (
        <form onSubmit={continueToPassword}>
          <h3>Sign in to Kairon</h3>
          <div className="formgrid">
            <label>
              Username
              <input required autoFocus value={user} onChange={(e) => setUser(e.target.value)} />
            </label>
          </div>
          <div className="formactions">
            <button className="primary" type="submit" disabled={!user}>
              Continue
            </button>
          </div>
        </form>
      ) : (
        <form onSubmit={signIn}>
          <h3>
            Hi, {user}.{' '}
            <button type="button" className="linklike" onClick={() => setStep('username')}>
              Not you?
            </button>
          </h3>
          <div className="formgrid">
            <label>
              Password
              <input required autoFocus type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </label>
          </div>
          <div className="formactions">
            <button className="primary" type="submit" disabled={busy || !password}>
              {busy ? 'Signing in...' : 'Sign In'}
            </button>
            {msg && <span className="msg error">{msg}</span>}
          </div>
        </form>
      )}
    </LoginChrome>
  );
}
