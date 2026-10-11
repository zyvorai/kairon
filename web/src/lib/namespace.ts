// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// The dashboard works in one namespace at a time. The server scopes every
// namespaced list/create route by the `namespace` query parameter and falls
// back to "default" when it is absent, so a client that never sends it can
// only ever see namespace "default". This module holds the selection and
// stamps it onto those routes.

const KEY = 'kairon-namespace';

// NAMESPACE_EVENT fires after the selection changes so the app can remount
// the visible page and re-fetch.
export const NAMESPACE_EVENT = 'kairon:namespace';

export function currentNamespace(): string {
  try {
    return localStorage.getItem(KEY) || 'default';
  } catch {
    return 'default';
  }
}

export function setNamespace(ns: string) {
  const next = ns || 'default';
  if (next === currentNamespace()) return;
  try {
    if (next === 'default') localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, next);
  } catch {
    // storage unavailable (private window): the selection simply won't persist
  }
  window.dispatchEvent(new Event(NAMESPACE_EVENT));
}

// Collections the server reads `?namespace=` on (internal/uiapi/server.go,
// routes using namespaceParam). Sub-paths such as /api/v1/machines/ns/name
// carry the namespace in the path and are left alone.
const NAMESPACED_COLLECTIONS = new Set([
  'machines',
  'migrations',
  'snapshots',
  'restores',
  'quotas',
  'machinepools',
  'machineclaims',
  'machinebackups',
  'machinebackuprestores',
  'disruption-budgets',
  'machinesets',
  'instancetypes',
  'migration-policies',
  'snapshot-schedules',
  'network-policies',
  'security-groups',
  'usage.csv',
]);

// withNamespace adds `namespace=<current>` to a namespaced collection path
// unless the caller already set one.
export function withNamespace(path: string, ns: string = currentNamespace()): string {
  const m = /^\/api\/v1\/([a-z.-]+)(\?.*)?$/.exec(path);
  if (!m || !NAMESPACED_COLLECTIONS.has(m[1])) return path;
  const query = new URLSearchParams(m[2] ? m[2].slice(1) : '');
  if (query.has('namespace')) return path;
  query.set('namespace', ns);
  return `/api/v1/${m[1]}?${query.toString()}`;
}
