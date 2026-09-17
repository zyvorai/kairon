// Badge color classification and formatting helpers shared by the
// Machines/Migrations/Snapshots pages. Phase strings are exactly what
// internal/agent and internal/controller set on status.phase -- see
// normalizePhase (Machine) and the migration state machine documented in
// docs/architecture.md (MachineMigration): Pending -> {Starting|Stopping}
// -> ... -> {Cutover->Adopting->Succeeded | Stopping->Restarting->Succeeded}
// | Blocked | Failed | NeedsRecovery | Cancelled (operator-requested, via
// spec.cancel, only reachable from Starting/Running).

export function badgeClass(phase: string): string {
  switch (phase) {
    case 'Running':
    case 'Succeeded':
    case 'True':
      return 'badge badge-ok';
    case 'Starting':
    case 'Cutover':
    case 'Adopting':
    case 'Stopping':
    case 'Restarting':
    case 'Pending':
      return 'badge badge-progress';
    case 'NeedsRecovery':
    case 'Paused':
      return 'badge badge-warn';
    case 'Failed':
    case 'Blocked':
    case 'Error':
      return 'badge badge-error';
    case 'Stopped':
    case 'Halted':
    case 'Cancelled':
      return 'badge badge-idle';
    default:
      return 'badge badge-idle';
  }
}

// formatBytes renders a byte count (Kairon reports RAM in bytes, e.g.
// status.ramTransferred/ramTotal) as a short human-readable string.
export function formatBytes(n?: number): string {
  if (!n || n <= 0) return '-';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  const decimals = i === 0 || v >= 10 || v % 1 === 0 ? 0 : 1;
  return `${v.toFixed(decimals)} ${units[i]}`;
}

// formatDuration renders a millisecond count (status.totalTimeMs/downtimeMs)
// as a short human-readable string.
export function formatDuration(ms?: number): string {
  if (!ms || ms <= 0) return '-';
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  const rem = Math.round(s % 60);
  return `${m}m${rem}s`;
}

export function transferProgress(transferred?: number, total?: number): number | null {
  if (!total || total <= 0) return null;
  const pct = ((transferred ?? 0) / total) * 100;
  return Math.max(0, Math.min(100, Math.round(pct)));
}
