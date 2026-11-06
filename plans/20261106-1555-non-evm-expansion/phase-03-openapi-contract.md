---
phase: 3
title: "OpenAPI contract — solana/tron marked unavailable"
status: completed
priority: P2
dependencies: [2]
---

# Phase 3: OpenAPI — add solana/tron to network enums marked unavailable

## Overview

Update `api/openapi.yaml` so the contract names the registered-but-inactive
codes honestly: `solana` and `tron` appear in the `network` enums with an
explicit **unavailable** marking and an example — matching existing spec
style. Runtime keeps rejecting them (fail-closed at the resolver).

## Requirements

- Functional:
  - `components.parameters.Network.schema.enum`:
    `[ethereum, bsc, solana, tron]` + description noting solana/tron are
    registered but **INACTIVE — requests return `400 INVALID_NETWORK`
    until enabled**.
  - The marking text MUST also state that the sibling `Address` path
    parameter's `pattern` (`^(0x)?[0-9a-fA-F]{40}$`, openapi.yaml L1636)
    remains EVM-only — the enum admits codes no request can currently
    exercise; make the unexercisable combination explicit rather than
    implicit (post-debate amendment).
  - Provider-probe request body `network` enum (openapi.yaml ~L1337):
    same extension + marking.
  - Add an example showing a solana request rejected with
    `INVALID_NETWORK` where spec style allows (e.g. the wallets endpoint
    error example or the parameter description example).
  - `/api/v1/networks` doc note: response lists **active** networks only —
    solana/tron do not appear until enabled.
- Non-functional: contract remains internally consistent — no enum value
  implies a working route; every mention is paired with the unavailable
  marker. No code behavior change (contract documentation only).

## Architecture

Single file edit, spec-style-preserving. The resolver remains the runtime
authority: enum admits the string, `Resolve` rejects it 400 — the same
honest-rejection pattern the API already uses for unknown codes.

## Related Code Files

- Create: —
- Modify: `apps/api/api/openapi.yaml`
- Delete: —

## Implementation Steps

1. Update `parameters.Network` enum + description (registered-but-inactive
   wording + example).
2. Update probe request-body `network` enum + description.
3. Update `/api/v1/networks` `200` description: "Active networks only;
   solana/tron are registered but inactive and therefore absent."
4. `Address` parameter description + `pattern` stay EVM-scoped — do NOT
   touch the pattern in this phase (groundwork does not generalize the
   address contract); the Network marking text documents this explicitly;
   the audit doc lists the pattern as a pre-enablement blocker.
5. Validate YAML parses (e.g. `go run` a yaml unmarshal or python yaml
   safe_load) and enum values consistent across both sites.

## Success Criteria

- [x] Both enums contain `[ethereum, bsc, solana, tron]` with unavailable
      marking text that explicitly names the EVM-only `Address` pattern
      constraint
- [x] Example/descriptor communicates `INVALID_NETWORK` rejection for the
      inactive codes
- [x] `/networks` doc states active-only listing
- [x] YAML parses cleanly; no other spec sections altered

## Risk Assessment

- **Partners misreading enum as supported**: mitigated by explicit
  unavailable marking + example of the 400 rejection; runtime is
  fail-closed anyway.
- **Contract contradiction**: enum admits solana/tron while `Address`
  `pattern` accepts only EVM hex — the marking text makes this
  unexercisable-by-design combination explicit (pattern generalization is
  a pre-enablement blocker documented in the audit doc).
- **Spec/test drift**: no code reads the enum — low risk; grep to confirm
  no validator enforces the enum list in tests.
