export const token = () => sessionStorage.getItem('kairon-token') || '';

export function setToken(v: string) {
  if (v) sessionStorage.setItem('kairon-token', v);
  else sessionStorage.removeItem('kairon-token');
}

function headers(extra: Record<string, string> = {}): Record<string, string> {
  const h: Record<string, string> = { ...extra };
  if (token()) h.Authorization = `Bearer ${token()}`;
  return h;
}

export async function api<T = unknown>(path: string, init: RequestInit = {}): Promise<T> {
  const r = await fetch(path, { ...init, headers: headers((init.headers as Record<string, string>) || {}) });
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
