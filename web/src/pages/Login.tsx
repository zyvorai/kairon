import { useEffect, useState } from 'react';
import { AlertCircle, Loader2 } from 'lucide-react';
import { AuthConfig, getAuthConfig, login, setToken } from '../api';

type Step = 'username' | 'password';

function Avatar({ user }: { user: string }) {
  return <div className="loginavatar">{user.charAt(0).toUpperCase() || '?'}</div>;
}

function LoginChrome({ children }: { children: React.ReactNode }) {
  return (
    <div className="loginwrap">
      <div className="loginsplit">
        <div className="loginsplit-left">
          <div className="loginorb" aria-hidden />
          <div className="loginbrand">
            <img className="dot" src="/zyvor-favicon.svg" alt="Zyvor" width={28} height={28} />
            <div>
              <div className="loginbrand-name">
                KAIRON <small>by Zyvor</small>
              </div>
              <p className="loginbrand-tagline">VM orchestration on FluxVM &mdash; no KubeVirt, no libvirt.</p>
            </div>
          </div>
        </div>
        <div className="loginsplit-right">
          <div className="card logincard">{children}</div>
          <p className="loginhost">
            Connecting to <code>{window.location.host}</code>
          </p>
        </div>
      </div>
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

  // SSO is offered alongside whichever other method(s) are configured
  // below, not instead of them -- a deployment can run ui.auth.users and
  // OIDC/SSO at once (see internal/uiapi/oidc.go), e.g. while migrating
  // operators from named accounts to a real identity provider.
  const sso = config.ssoEnabled && config.ssoLoginURL && (
    <div className="loginstep">
      <button type="button" className="primary" onClick={() => (window.location.href = config.ssoLoginURL!)}>
        Sign in with SSO
      </button>
    </div>
  );

  if (!config.loginEnabled) {
    if (!config.tokenEnabled) {
      return (
        <LoginChrome>
          <span className="eyebrow">SIGN IN</span>
          {sso}
          {!sso && <p>No login method is configured on this server.</p>}
        </LoginChrome>
      );
    }
    return (
      <LoginChrome>
        <span className="eyebrow">SIGN IN</span>
        {sso}
        {sso && <p className="loginor">or</p>}
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
      {sso}
      {sso && <p className="loginor">or</p>}
      {step === 'username' ? (
        <div key="username" className="loginstep">
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
        </div>
      ) : (
        <div key="password" className="loginstep">
          <form onSubmit={signIn}>
            <h3 className="loginwho">
              <Avatar user={user} />
              <span>
                Hi, {user}.{' '}
                <button type="button" className="linklike" onClick={() => setStep('username')}>
                  Not you?
                </button>
              </span>
            </h3>
            <div className="formgrid">
              <label>
                Password
                <input required autoFocus type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
              </label>
            </div>
            <div className="formactions">
              <button className="primary" type="submit" disabled={busy || !password}>
                {busy && <Loader2 size={15} className="spin" />}
                {busy ? 'Signing in...' : 'Sign In'}
              </button>
            </div>
            {msg && (
              <div className="loginerror">
                <AlertCircle size={16} />
                <span>{msg}</span>
              </div>
            )}
          </form>
        </div>
      )}
    </LoginChrome>
  );
}
