-- +goose Up
-- The webhook "Default Priority" selector previously used an inverted scale
-- (0=Urgent...4=Backlog) relative to the canonical task priority scale
-- (1=Low, 2=Normal, 3=High, 4=Urgent). Existing endpoints with the legacy
-- default_priority=0 ("Urgent" under the old scale) would silently produce
-- badge-less, bottom-sorted tasks under the canonical scale. Remap them to
-- Normal (2) so stored values stay within the canonical 1-4 range.
UPDATE webhook_endpoints SET default_priority = 2 WHERE default_priority < 1;

-- +goose Down
-- Not reversible: the original (legacy-scale) default_priority=0 values are
-- not recoverable once remapped.
