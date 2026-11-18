-- +goose Up
ALTER TABLE project_computers ADD COLUMN cpu_count INTEGER NOT NULL DEFAULT 2 CHECK(cpu_count BETWEEN 1 AND 64);
ALTER TABLE project_computers ADD COLUMN memory_gib INTEGER NOT NULL DEFAULT 4 CHECK(memory_gib BETWEEN 1 AND 256);
ALTER TABLE project_computers ADD COLUMN pids_limit INTEGER NOT NULL DEFAULT 4096 CHECK(pids_limit BETWEEN 256 AND 65536);

-- +goose Down
ALTER TABLE project_computers DROP COLUMN pids_limit;
ALTER TABLE project_computers DROP COLUMN memory_gib;
ALTER TABLE project_computers DROP COLUMN cpu_count;
