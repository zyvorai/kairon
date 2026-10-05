// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

import { FormEvent, useState } from 'react';
import { apiJSON } from '../api';

interface AssistResult {
  kind: 'policy' | 'claim' | 'repair' | 'explain';
  summary: string;
  explanation?: string;
  policy?: unknown;
  intent?: unknown;
  claim?: unknown;
  repair?: { code: string; action: string }[];
  attempts: number;
  apply: boolean;
}

// errorText unwraps the {"error": "..."} body the agent routes return.
function errorText(e: unknown): string {
  const s = String(e instanceof Error ? e.message : e);
  try {
    return JSON.parse(s).error || s;
  } catch {
    return s;
  }
}

export default function Assistant() {
  const [form, setForm] = useState({ question: '', tenant: '', namespace: '', facts: '' });
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState('');
  const [result, setResult] = useState<AssistResult | null>(null);

  async function ask(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMsg('');
    setResult(null);
    try {
      setResult(await apiJSON<AssistResult>('/api/v1/agent/ask', 'POST', form));
    } catch (err) {
      setMsg(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  const proposal = result && (result.policy || result.claim || result.repair);

  return (
    <div className="grid">
      <div className="card span4">
        <span className="eyebrow">AGENT PLANE · ASSISTANT</span>
        <h3>Ask for a proposal</h3>
        <p>
          The configured model proposes an egress policy, a sealed claim, boot repair steps or an explanation. Kairon compiles and validates the answer before
          showing it. Nothing here is applied.
        </p>
        <form onSubmit={ask}>
          <div className="formgrid">
            <label className="full">
              Question
              <input
                required
                value={form.question}
                onChange={(e) => setForm({ ...form, question: e.target.value })}
                placeholder="let job-7 reach pypi and our registry for 2h"
              />
            </label>
            <label>
              Tenant
              <input value={form.tenant} onChange={(e) => setForm({ ...form, tenant: e.target.value })} placeholder="forced onto the proposal" />
            </label>
            <label>
              Namespace
              <input value={form.namespace} onChange={(e) => setForm({ ...form, namespace: e.target.value })} placeholder="forced onto the proposal" />
            </label>
            <label className="full">
              Facts (optional)
              <textarea rows={4} value={form.facts} onChange={(e) => setForm({ ...form, facts: e.target.value })} placeholder="paste drops, status or events" />
            </label>
          </div>
          <div className="formactions">
            <button className="primary" type="submit" disabled={busy}>
              {busy ? 'Asking...' : 'Ask'}
            </button>
          </div>
        </form>
        {msg && <p className="msg error">{msg}</p>}
      </div>
      {result && (
        <div className="card span4">
          <span className="eyebrow">
            PROPOSAL · {result.kind.toUpperCase()} · {result.attempts === 1 ? 'valid first try' : `valid after ${result.attempts} attempts`} · not applied
          </span>
          <h3>{result.summary}</h3>
          {result.explanation && <p>{result.explanation}</p>}
          {proposal && <pre className="execStream">{JSON.stringify(proposal, null, 2)}</pre>}
        </div>
      )}
    </div>
  );
}
