-- +goose Up
-- Baseline schema. app_meta is a tiny key/value table used for schema/runtime
-- bookkeeping and to give sqlc + goose a concrete schema to operate on.
-- Real domain tables (services, hosts, probe samples) arrive in issue 0002.
CREATE TABLE app_meta (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);

-- +goose Down
DROP TABLE app_meta;
