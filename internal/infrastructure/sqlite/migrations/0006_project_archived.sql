-- 0006_project_archived: store archived flag per project.

ALTER TABLE project ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;
