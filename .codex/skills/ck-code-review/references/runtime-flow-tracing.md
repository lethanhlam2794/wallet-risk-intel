---
name: runtime-flow-tracing
description: Trace cross-module runtime behavior before accepting API, workflow, or persisted-outcome claims
---

# Runtime Flow Tracing

## Purpose

Verify what the system can actually do across runtime boundaries. Do not infer an
outcome from endpoint names, comments, isolated handlers, mocked happy paths, or a
downstream state update that upstream execution may never reach.

This is static/dynamic end-to-end **flow tracing**. It does not automatically require
running browser or system E2E tests; select executable tests from the risk and changed
contract after the trace identifies the real boundaries.

## When Mandatory

Trace the runtime flow before accepting claims about:

- Cross-module features or changes spanning three or more files
- External APIs, brokers, payment providers, queues, workers, or daemons
- Authentication, authorization, tenant/account ownership, or IDOR protection
- Financial mutations, durable jobs, retries, reconciliation, or terminal states
- APIs, jobs, events, metrics, or dashboards whose data has an upstream writer

## Minimum Trace

Follow the concrete path in both directions:

```text
entrypoint / trigger
→ authentication and authorization context
→ validated request or message contract
→ service and ownership filters
→ persistence state / schema
→ worker, queue, or external adapter
→ runtime response / exception contract
→ caller interpretation
→ persisted or externally visible outcome
```

For read paths, continue upstream from the queried record to the job, event, callback,
or transaction that creates and mutates it.

## Review Procedure

1. Identify the real entrypoint or trigger. If none exists, report that the behavior is
   not runtime-reachable instead of assuming a future caller.
2. Prove identity and ownership sources. Treat TypeScript types, DTO fields, emails, and
   caller-supplied IDs as data, not authentication evidence.
3. Record each boundary contract: input, output, error shape, timeout, retry, and state
   transition semantics.
4. Trace success, explicit rejection, timeout/disconnect, malformed response, crash
   before send, crash after send, and duplicate/concurrent invocation when applicable.
5. Check whether adapters flatten distinct outcomes, such as converting authoritative
   rejection and transport uncertainty into the same `ok: false` response.
6. Verify terminal state claims against the authoritative source of truth. Do not accept
   provisional acknowledgements, UI text, inferred deltas, or mock-only evidence.
7. Compare code, tests, entity/DDL constraints, queue/driver contracts, and operational
   flags at every boundary. Flag contradictions even when each layer passes alone.
8. Run the narrowest executable test that crosses the risky boundary. Require real E2E
   or integration tests when mocks cannot prove the runtime contract.

## Evidence Format

For each behavior claim, record:

```text
Claim: <requested behavior>
Runtime path: <entrypoint → ... → outcome>
Boundary evidence: <files, lines, tests, runtime output>
Negative paths checked: <errors, timeout, crash, concurrency>
Verdict: PROVEN | PARTIAL | NOT_REACHABLE | CONTRADICTED
Verification gap: <missing integration/E2E evidence, if any>
```

## Stop Conditions

- Do not claim an API or workflow works when its entrypoint, authentication context, or
  upstream writer cannot be found.
- Do not mark a financial/external operation terminal from ambiguous transport evidence.
- Do not let passing unit tests override a runtime contract mismatch introduced by mocks.
- Do not demand E2E tests perform irreversible external mutations; use sandbox/read-only
  verification or report the explicit evidence gap.
