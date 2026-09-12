export const token = () => sessionStorage.getItem('kairon-token') || '';
export const username = () => sessionStorage.getItem('kairon-username') || '';

export function setSession(tok: string, user: string) {
  if (tok) sessionStorage.setItem('kairon-token', tok);
  else sessionStorage.removeItem('kairon-token');
  if (user) sessionStorage.setItem('kairon-username', user);
  else sessionStorage.removeItem('kairon-username');
}

// setToken keeps the legacy raw-token entry point (still used by the
// fallback token box when username/password login isn't configured on the
// server) -- it never carries a username.
export function setToken(v: string) {
  setSession(v, '');
}

function headers(extra: Record<string, string> = {}): Record<string, string> {
  const h: Record<string, string> = { ...extra };
  if (token()) h.Authorization = `Bearer ${token()}`;
  return h;
}

// UNAUTHORIZED_EVENT fires whenever any API call gets a 401, so App.tsx can
// clear the stale session and bounce back to the login screen in one place,
// rather than every caller having to check for it individually.
export const UNAUTHORIZED_EVENT = 'kairon:unauthorized';

export async function api<T = unknown>(path: string, init: RequestInit = {}): Promise<T> {
  const r = await fetch(path, { ...init, headers: headers((init.headers as Record<string, string>) || {}) });
  if (r.status === 401) {
    window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
  }
  if (!r.ok) throw new Error((await r.text()) || r.statusText);
  if (r.status === 204) return undefined as T;
  return r.json();
}

export function apiJSON<T = unknown>(path: string, method: string, body: unknown): Promise<T> {
  return api<T>(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
}

export interface AuthConfig {
  loginEnabled: boolean;
  tokenEnabled: boolean;
}

// getAuthConfig is unauthenticated by design (see internal/uiapi/auth.go),
// so the login screen can decide which form to render before any
// credential exists yet.
export async function getAuthConfig(): Promise<AuthConfig> {
  const r = await fetch('/api/v1/auth/config');
  if (!r.ok) throw new Error(await r.text());
  return r.json();
}

export async function login(usernameInput: string, password: string): Promise<void> {
  const r = await fetch('/api/v1/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: usernameInput, password }),
  });
  if (!r.ok) throw new Error((await r.text()) || r.statusText);
  const out = await r.json();
  setSession(out.token, out.username);
}

export async function logout(): Promise<void> {
  try {
    await api('/api/v1/auth/logout', { method: 'POST' });
  } finally {
    setSession('', '');
  }
}

export interface Config {
  consoleEnabled: boolean;
}

// getConfig reports small feature toggles (currently just consoleEnabled)
// so the UI can decide what to render -- e.g. hiding the "Console" button
// entirely when the deployment doesn't have it configured, rather than
// showing it and only failing after a click.
export function getConfig(): Promise<Config> {
  return api<Config>('/api/v1/config');
}

// getConsoleWebSocketURL fetches a short-lived, single-use ticket (a
// normal authenticated fetch()) and builds the VNC console WebSocket URL
// from it -- a native browser WebSocket can't carry an Authorization
// header, so the real session token never appears in this URL, only the
// disposable ticket does (see internal/uiapi/console.go).
export async function getConsoleWebSocketURL(namespace: string, name: string): Promise<string> {
  const out = await api<{ ticket: string }>(`/api/v1/machines/${namespace}/${encodeURIComponent(name)}/console/ticket`, { method: 'POST' });
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/api/v1/machines/${namespace}/${encodeURIComponent(name)}/console?ticket=${encodeURIComponent(out.ticket)}`;
}
