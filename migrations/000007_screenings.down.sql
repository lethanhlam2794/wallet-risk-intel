-- Migration 000007 down: drop screening persistence. screening_requests
-- first — it holds the FK to screening_results.

DROP TABLE IF EXISTS screening_requests;
DROP TABLE IF EXISTS screening_results;
