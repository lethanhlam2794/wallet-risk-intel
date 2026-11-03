---
name: review-rulebook
description: Reusable invariant checks for schema, persistence, and runtime-contract reviews
---

# Review Rulebook

## Typed-to-JSONB Constraint Preservation

When a typed relational field is moved into JSONB, compare and preserve
nullability, type, precision, foreign-key scope, uniqueness, and CHECK semantics.

Required review actions:

1. Construct the smallest missing-key, JSON-null, wrong-type, zero, boundary,
   duplicate, and wrong-owner counterexamples.
2. Evaluate PostgreSQL CHECK expressions under TRUE, FALSE, and UNKNOWN.
   PostgreSQL rejects a CHECK only when its result is FALSE; UNKNOWN is
   accepted, so missing JSONB keys must be converted to an explicit FALSE.
3. Verify DTO validation and database validation independently. HTTP validation
   does not protect migration, raw SQL, worker, or internal-service writers.
4. Treat repository mocks as insufficient evidence for CHECK constraints,
   foreign keys, indexes, locks, and driver behavior. A skipped integration
   test is a verification gap.
5. Compare every old DDL guarantee with its new JSONB equivalent:
   `NOT NULL` requires key existence and non-null value; typed numeric columns
   require JSON number and precision semantics; foreign keys require scoped
   identity checks; unique indexes require equivalent partial/expression rules.
6. Enumerate every writer and consumer before marking the invariant PASS.

Comments and plan claims are hypotheses. They are not evidence until the SQL,
runtime contract, or dedicated integration test verifies them.
