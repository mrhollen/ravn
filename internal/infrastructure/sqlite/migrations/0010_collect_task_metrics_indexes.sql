-- 0010_collect_task_metrics_indexes: Partial indexes for the two /metrics
-- scrape queries in CollectTaskMetrics (internal/infrastructure/sqlite/metrics.go).
-- Every existing index on task leads with project_id, so these queries had no
-- partial-scan option and fell back to scanning the whole table (or a whole
-- non-covering index plus a temp B-tree for GROUP BY).
--
--   idx_task_metrics_status  serves:
--     SELECT status, COUNT(*) FROM task WHERE archived = 0 GROUP BY status
--     (covering: the partial predicate matches `archived = 0` and status is the key)
--
--   idx_task_active_claims   serves:
--     SELECT COUNT(*) FROM task WHERE claimed_by_name IS NOT NULL AND (claim_expires_at IS NULL OR claim_expires_at >= ?)
--     (partial predicate matches `claimed_by_name IS NOT NULL`; range on claim_expires_at)
CREATE INDEX IF NOT EXISTS idx_task_metrics_status ON task(status) WHERE archived = 0;
CREATE INDEX IF NOT EXISTS idx_task_active_claims ON task(claim_expires_at) WHERE claimed_by_name IS NOT NULL;
