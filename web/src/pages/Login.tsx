// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { AlertCircle, ArrowRight, Loader2 } from 'lucide-react';
import { AuthConfig, getAuthConfig, login, setToken } from '../api';

type Step = 'username' | 'password';

function Avatar({ user }: { user: string }) {
  return <div className="loginavatar">{user.charAt(0).toUpperCase() || '?'}</div>;
}

function LoginChrome({ children }: { children: React.ReactNode }) {
  return (
    <div className="loginwrap force-light">
      <div className="loginsplit">
        <div className="loginsplit-left">
          <div className="loginorb" aria-hidden />
          <div className="loginbrand">
            <img className="dot" src="/zyvor-favicon.svg" alt="Zyvor" width={40} height={40} />
            <p className="login-kicker">Kairon &middot; Zyvor</p>
            <h1 className="login-title">Real VMs.<br />One console.</h1>
            <p className="loginbrand-tagline">VM orchestration on FluxVM &mdash; no KubeVirt, no libvirt.</p>
          </div>
        </div>
        <div className="loginsplit-right">
          <div className="logincard">{children}</div>
          <p className="loginhost">
            Connecting to <code>{window.location.host}</code>
          </p>
        </div>
      </div>
    </div>
  );
}

function Field({
  label,
  value,
  onChange,
  type = 'text',
  autoFocus,
  autoComplete,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
  autoFocus?: boolean;
  autoComplete?: string;
}) {
  return (
    <label className={'siw-field' + (value ? ' filled' : '')}>
      <input
        required
        type={type}
        autoFocus={autoFocus}
        autoComplete={autoComplete}
        value={value}
        placeholder=" "
        onChange={(e) => onChange(e.target.value)}
      />
      <span>{label}</span>
    </label>
  );
}

export default function Login({ onSignedIn }: { onSignedIn: () => void }) {
  const [config, setConfig] = useState<AuthConfig | null>(null);
  const [step, setStep] = useState<Step>('username');
  const [user, setUser] = useState('');
  const [password, setPassword] = useState('');
  const [rawToken, setRawToken] = useState('');
  const [useTokenForm, setUseTokenForm] = useState(false);
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

  function submitToken(e: React.FormEvent) {
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
      <button type="button" className="primary wide" onClick={() => (window.location.href = config.ssoLoginURL!)}>
        Sign in with SSO
      </button>
    </div>
  );

  const tokenForm = (
    <form onSubmit={submitToken} className="loginstep">
      <h3>Sign in with an API token.</h3>
      <div className="siw">
        <Field label="API token" type="password" autoFocus value={rawToken} onChange={setRawToken} autoComplete="off" />
      </div>
      <div className="formactions">
        <button className="primary wide" type="submit" disabled={!rawToken && !config.tokenEnabled}>
          Continue
        </button>
      </div>
    </form>
  );

  if (!config.loginEnabled) {
    if (!config.tokenEnabled) {
      return (
        <LoginChrome>
          {sso}
          {!sso && <p className="logincopy">No login method is configured on this server.</p>}
        </LoginChrome>
      );
    }
    return (
      <LoginChrome>
        {sso}
        {sso && <p className="loginor">or</p>}
        {tokenForm}
      </LoginChrome>
    );
  }

  return (
    <LoginChrome>
      {sso}
      {sso && <p className="loginor">or</p>}
      {useTokenForm ? (
        <>
          {tokenForm}
          <p className="loginfoot">
            <button type="button" className="linklike" onClick={() => setUseTokenForm(false)}>
              Use username and password
            </button>
          </p>
        </>
      ) : step === 'username' ? (
        <div key="username" className="loginstep">
          <form onSubmit={continueToPassword}>
            <h3>Sign in to Kairon.</h3>
            <div className="siw">
              <Field label="Username" autoFocus autoComplete="username" value={user} onChange={setUser} />
              <button className="siw-go" type="submit" disabled={!user} aria-label="Continue">
                <ArrowRight size={16} />
              </button>
            </div>
          </form>
          {config.tokenEnabled && (
            <p className="loginfoot">
              <button type="button" className="linklike" onClick={() => setUseTokenForm(true)}>
                Use an API token instead
              </button>
            </p>
          )}
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
            <div className="siw">
              <Field label="Password" type="password" autoFocus autoComplete="current-password" value={password} onChange={setPassword} />
              <button className="siw-go" type="submit" disabled={busy || !password} aria-label="Sign in">
                {busy ? <Loader2 size={16} className="spin" /> : <ArrowRight size={16} />}
              </button>
            </div>
            {msg && (
              <div className="loginerror" role="alert">
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
