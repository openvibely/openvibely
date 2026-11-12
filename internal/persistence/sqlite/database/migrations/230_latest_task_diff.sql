-- +goose Up
-- Keep the same fallback selected by GetLatestNonEmptyDiffOutput. Rank only
-- non-empty values so a newer empty execution cannot discard that fallback.
WITH ranked AS (
    SELECT rowid, ROW_NUMBER() OVER (
        PARTITION BY task_id ORDER BY started_at DESC, rowid DESC
    ) AS position
    FROM executions WHERE diff_output != ''
)
UPDATE executions SET diff_output = ''
WHERE rowid IN (SELECT rowid FROM ranked WHERE position > 1);

-- +goose Down
-- Historical diff payloads cannot be restored. Execution rows are unchanged.
SELECT 1;
