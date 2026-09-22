# OpenSSF Best Practices badge

Scorecard's `CII-Best-Practices` check looks up this GitHub repo in the
[OpenSSF Best Practices](https://www.bestpractices.dev/) API. A project
entry must exist there (even at 0% / "in progress") before the score moves
off zero.

## One-time registration (human GitHub OAuth)

1. Open: https://www.bestpractices.dev/en/projects/new?url=https%3A%2F%2Fgithub.com%2Fzyvorai%2Fkairon
2. Sign in with the `zyvorai` GitHub org account that can claim the repo.
3. Create the project (name: **Kairon**).
4. Copy the numeric project id from the URL (`/projects/<id>`).
5. Open a PR that adds this badge next to the others in [`README.md`](../README.md):

```markdown
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/<id>/badge)](https://www.bestpractices.dev/projects/<id>)
```

6. Work the passing-tier questionnaire (many answers are already true for
   this repo: Apache-2.0, `SECURITY.md`, CI, CodeQL, release signing, etc.).

Passing tier → Scorecard score 5; silver 7; gold 10; in-progress alone → 2.

## Maintained check

Scorecard `Maintained` stays at 0 until the repository is **older than 90
days** (created 2026-09-11 → eligible ~2026-12-10), then needs roughly
weekly commits. No code change can advance that calendar.
