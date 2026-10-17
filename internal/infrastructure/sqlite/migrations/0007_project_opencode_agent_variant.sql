-- 0007_project_opencode_agent_variant: store optional OpenCode agent and model variant per project.
-- opencode_agent defaults to '' (meaning use $OPENCODE_AGENT or built-in "build").
-- opencode_variant defaults to '' (meaning use $OPENCODE_VARIANT or omit variant).

ALTER TABLE project ADD COLUMN opencode_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE project ADD COLUMN opencode_variant TEXT NOT NULL DEFAULT '';
