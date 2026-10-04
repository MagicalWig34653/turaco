CREATE INDEX IF NOT EXISTS tasks_open_due_idx ON platform.tasks(context_type)
WHERE status NOT IN ('completed','cancelled') AND due_at IS NOT NULL AND context_id IS NOT NULL;
