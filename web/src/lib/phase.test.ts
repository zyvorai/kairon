import { describe, it, expect } from 'vitest';
import { badgeClass, formatBytes, formatDuration, transferProgress } from './phase';

describe('badgeClass', () => {
  it('marks terminal-success phases ok', () => {
    expect(badgeClass('Running')).toBe('badge badge-ok');
    expect(badgeClass('Succeeded')).toBe('badge badge-ok');
  });
  it('marks in-flight phases progress', () => {
    expect(badgeClass('Cutover')).toBe('badge badge-progress');
    expect(badgeClass('Adopting')).toBe('badge badge-progress');
  });
  it('marks NeedsRecovery warn, not error', () => {
    expect(badgeClass('NeedsRecovery')).toBe('badge badge-warn');
  });
  it('marks Failed and Blocked error', () => {
    expect(badgeClass('Failed')).toBe('badge badge-error');
    expect(badgeClass('Blocked')).toBe('badge badge-error');
  });
  it('falls back to idle for unknown phases', () => {
    expect(badgeClass('SomethingNew')).toBe('badge badge-idle');
  });
});

describe('formatBytes', () => {
  it('renders dash for empty/zero', () => {
    expect(formatBytes(undefined)).toBe('-');
    expect(formatBytes(0)).toBe('-');
  });
  it('scales through units', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(2048)).toBe('2 KiB');
    expect(formatBytes(5 * 1024 * 1024)).toBe('5 MiB');
    expect(formatBytes(1.5 * 1024 * 1024 * 1024)).toBe('1.5 GiB');
  });
});

describe('formatDuration', () => {
  it('renders dash for empty/zero', () => {
    expect(formatDuration(undefined)).toBe('-');
    expect(formatDuration(0)).toBe('-');
  });
  it('renders milliseconds under one second', () => {
    expect(formatDuration(500)).toBe('500ms');
  });
  it('renders seconds under one minute', () => {
    expect(formatDuration(1500)).toBe('1.5s');
  });
  it('renders minutes and seconds over one minute', () => {
    expect(formatDuration(90000)).toBe('1m30s');
  });
});

describe('transferProgress', () => {
  it('returns null when total is unknown', () => {
    expect(transferProgress(10, 0)).toBeNull();
    expect(transferProgress(10, undefined)).toBeNull();
  });
  it('computes a clamped percentage', () => {
    expect(transferProgress(50, 200)).toBe(25);
    expect(transferProgress(300, 200)).toBe(100);
    expect(transferProgress(-10, 200)).toBe(0);
  });
});
