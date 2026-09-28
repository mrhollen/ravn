-- 0005_project_opencode_model: store the optional OpenCode model per project.
-- Format "providerID/modelID" (e.g. "llama.cpp/Qwen Medium Long Context");
-- empty = let the OpenCode server choose its own default.

ALTER TABLE project ADD COLUMN opencode_model TEXT NOT NULL DEFAULT '';
