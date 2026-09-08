# Branching & Release Model

*For contributors — how branches and releases work in this repository. Not needed to use the operator.*

This project uses a two-branch model: a stable branch (`main`) and an
integration branch (`dev`).

## Branches

| Branch | Purpose | Direct PR target for |
|--------|---------|----------------------|
| `main` | Always reflects the latest released (or hotfix-pending) state. Stable and final release tags are cut from here. | Bug fixes for the current release (`fix:`, `docs:`, `perf:` on existing behaviour) |
| `dev`  | Accumulates the next feature release. May contain unreleased, still-changing APIs. | New functionality (`feat:`), anything touching `spec.alpha.*` / `v1alpha1`, breaking changes |

## Flow

```
feat/my-feature ──PR──▶ dev ──(RC cut, then)──▶ main ──tag──▶ release
fix/some-bug    ──PR──▶ main ──tag──▶ patch release
```

### New functionality

1. Branch from `dev`, open the PR **against `dev`**.
2. It merges into `dev` once CI is green and review is done.
3. `dev` keeps collecting features until the maintainer decides the next
   minor/major is ready.
4. The maintainer cuts a release candidate from `dev` (`vX.Y.0-rc.N` tag).
5. After the RC is validated, `dev` is merged into `main` and the final
   `vX.Y.0` tag is cut from `main`.

### Bug fixes

Fixes for the **currently released version** always go straight to `main`,
even while a `dev` cycle is in progress:

1. Branch from `main`, open the PR **against `main`**.
2. On merge, the maintainer cuts a patch release (`vX.Y.Z`) from `main`.
3. The maintainer then merges `main` back into `dev` so the fix is not lost
   in the next feature release and `dev` does not drift.

Once the next feature version has shipped (`dev` → `main` completed), there is
nothing special about the state — fixes simply continue to target `main`, and
the next feature cycle starts fresh on `dev`.

## Keeping `dev` in sync with `main`

After every release cut from `main` (patch or final), the maintainer merges
`main` back into `dev` so the released fixes are carried into the next feature
cycle and `dev` does not drift:

```bash
git checkout dev
git pull --ff-only
git merge --no-ff main
git push origin dev
```

Resolve any conflicts in favour of keeping both the fix and the in-progress
feature work. Never rebase `dev` onto `main` or force-push either branch —
both are shared.

## Automated dependency updates (Renovate)

Renovate (`renovate.json`) watches **both** long-lived branches
(`baseBranches: ["main", "dev"]`) and opens separate PRs per branch:

- **`main`** receives only non-breaking updates — `minor`, `patch`, `digest`,
  `pin`. These merge like any other fix and ship as a patch release; the
  `main` → `dev` sync above then carries them forward.
- **`dev`** additionally receives `major` / breaking dependency bumps, so they
  are integrated and tested during the feature cycle and only reach `main`
  through an RC — never as a surprise on the released line.
- Security fixes (`osvVulnerabilityAlerts`) are raised against whichever
  branch carries the vulnerable version, without waiting for a release cycle.

When `main` and `dev` hold the same dependency state you will get two
near-identical PRs; merge the `main` one, and the routine `git merge --no-ff
main` into `dev` makes Renovate close the redundant `dev` PR on its next run.

## Release candidates

- RC tags follow SemVer prerelease syntax: `v1.4.0-rc.1`, `v1.4.0-rc.2`, …
- RC tags are cut from `dev` (the only case where a tag does not come from `main`).
- Prerelease status on the GitHub release:
    - **Expected path** — after pushing the tag, create the release in the
      GitHub UI and **save it as a draft** with "Set as a pre-release" ticked
      for an RC. The release workflow attaches the signed checksums and
      publishes that draft as-is; whatever prerelease/latest flags the draft
      carries are preserved, so the draft must be configured correctly.
    - **Fallback path** — if no draft exists when the workflow runs (race lost,
      or skipped for a quick release), it creates the release fresh and derives
      prerelease status from the tag: a SemVer prerelease identifier
      (`vX.Y.Z-…`) is marked as a pre-release, anything else is not.
- Only after the RC is accepted does `dev` merge into `main` and the final
  tag get cut.
