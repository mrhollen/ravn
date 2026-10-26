-- 0009_task_order_due_nulls: Recreate idx_task_order to include (due_at IS NULL) ASC
-- in the index columns, matching the canonical ORDER BY used in task queries:
-- ORDER BY priority DESC, (due_at IS NULL), due_at ASC, created_at ASC.
-- This allows SQLite to scan idx_task_order directly without emitting
-- USE TEMP B-TREE FOR LAST 3 TERMS OF ORDER BY.
DROP INDEX IF EXISTS idx_task_order;
CREATE INDEX idx_task_order ON task(project_id, priority DESC, (due_at IS NULL) ASC, due_at ASC, created_at ASC) WHERE archived = 0;
