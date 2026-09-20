-- edge_evidence is joined by its edge identity on every relationship and
-- reference query, but SQLite does not index a foreign key. Without this
-- index the planner scans the whole evidence table once per matching edge
-- row, so query cost grows with retained generations instead of with the
-- generation under inspection.
CREATE INDEX IF NOT EXISTS idx_edge_evidence_edge
    ON edge_evidence (repo_id, worktree_id, generation_id, source_key, target_key, edge_kind);
