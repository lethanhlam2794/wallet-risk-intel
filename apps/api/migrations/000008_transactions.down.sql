DROP TABLE IF EXISTS wallet_index_state;
DROP TABLE IF EXISTS transactions;
DELETE FROM data_sources WHERE code IN ('chainscan', 'nodereal');
