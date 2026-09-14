import { describe, it, expect } from 'vitest';
import { parseSSOCallbackFragment } from './api';

describe('parseSSOCallbackFragment', () => {
  it('extracts token and username on success', () => {
    const result = parseSSOCallbackFragment('#token=abc123&username=alice%40example.com&expiresAt=2026-01-01T00%3A00%3A00Z');
    expect(result).toEqual({ token: 'abc123', username: 'alice@example.com' });
  });

  it('works without a leading #', () => {
    const result = parseSSOCallbackFragment('token=abc123&username=alice');
    expect(result).toEqual({ token: 'abc123', username: 'alice' });
  });

  it('surfaces an upstream error verbatim', () => {
    const result = parseSSOCallbackFragment('#error=access_denied');
    expect(result).toEqual({ error: 'access_denied' });
  });

  it('errors when the token is missing', () => {
    const result = parseSSOCallbackFragment('#username=alice');
    expect('error' in result).toBe(true);
  });

  it('errors when the username is missing', () => {
    const result = parseSSOCallbackFragment('#token=abc123');
    expect('error' in result).toBe(true);
  });

  it('errors on an empty fragment', () => {
    const result = parseSSOCallbackFragment('');
    expect('error' in result).toBe(true);
  });
});
