-- 0004_project_opencode: store optional OpenCode server URL and worktree directory per project.

ALTER TABLE project ADD COLUMN opencode_server_url TEXT NOT NULL DEFAULT '';
ALTER TABLE project ADD COLUMN opencode_directory TEXT NOT NULL DEFAULT '';
