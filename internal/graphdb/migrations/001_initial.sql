CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TEXT NOT NULL
);

CREATE TABLE repositories (
    repo_id TEXT PRIMARY KEY CHECK (length(repo_id) > 0),
    canonical_identity TEXT NOT NULL UNIQUE CHECK (length(canonical_identity) > 0),
    root_path TEXT NOT NULL,
    common_dir TEXT NOT NULL,
    remote_name TEXT NOT NULL DEFAULT '',
    remote_url TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE worktrees (
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL CHECK (length(worktree_id) > 0),
    worktree_path TEXT NOT NULL,
    git_dir TEXT NOT NULL,
    common_dir TEXT NOT NULL,
    branch TEXT NOT NULL DEFAULT '',
    observed_head TEXT NOT NULL DEFAULT '',
    head_known INTEGER NOT NULL DEFAULT 0 CHECK (head_known IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (repo_id, worktree_id),
    UNIQUE (repo_id, worktree_path),
    FOREIGN KEY (repo_id) REFERENCES repositories (repo_id) ON DELETE CASCADE
);

CREATE TABLE graph_generations (
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    generation_id INTEGER NOT NULL CHECK (generation_id > 0),
    commit_sha TEXT NOT NULL DEFAULT '',
    build_fingerprint TEXT NOT NULL DEFAULT '',
    analyzer_version TEXT NOT NULL DEFAULT '',
    schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version > 0),
    state TEXT NOT NULL CHECK (state IN ('building', 'validated', 'active', 'retired', 'superseded', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    validated_at TEXT,
    retired_at TEXT,
    PRIMARY KEY (repo_id, worktree_id, generation_id),
    FOREIGN KEY (repo_id, worktree_id) REFERENCES worktrees (repo_id, worktree_id) ON DELETE CASCADE
);

CREATE TABLE graph_state (
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    active_generation INTEGER,
    indexed_head TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('unindexed', 'building', 'ready', 'stale', 'failed')),
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (repo_id, worktree_id),
    FOREIGN KEY (repo_id, worktree_id) REFERENCES worktrees (repo_id, worktree_id) ON DELETE CASCADE,
    FOREIGN KEY (repo_id, worktree_id, active_generation)
        REFERENCES graph_generations (repo_id, worktree_id, generation_id)
);

CREATE TABLE blobs (
    repo_id TEXT NOT NULL,
    blob_sha TEXT NOT NULL,
    object_format TEXT NOT NULL DEFAULT 'sha1',
    byte_size INTEGER NOT NULL DEFAULT 0 CHECK (byte_size >= 0),
    created_at TEXT NOT NULL,
    PRIMARY KEY (repo_id, blob_sha),
    FOREIGN KEY (repo_id) REFERENCES repositories (repo_id) ON DELETE CASCADE
);

CREATE TABLE packages (
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    generation_id INTEGER NOT NULL,
    package_key TEXT NOT NULL,
    import_path TEXT NOT NULL DEFAULT '',
    module_path TEXT NOT NULL DEFAULT '',
    directory TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_id, worktree_id, generation_id, package_key),
    FOREIGN KEY (repo_id, worktree_id, generation_id)
        REFERENCES graph_generations (repo_id, worktree_id, generation_id) ON DELETE CASCADE
);

CREATE TABLE files (
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    generation_id INTEGER NOT NULL,
    file_key TEXT NOT NULL,
    path TEXT NOT NULL,
    blob_sha TEXT NOT NULL DEFAULT '',
    package_key TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_id, worktree_id, generation_id, file_key),
    UNIQUE (repo_id, worktree_id, generation_id, path),
    FOREIGN KEY (repo_id, worktree_id, generation_id)
        REFERENCES graph_generations (repo_id, worktree_id, generation_id) ON DELETE CASCADE
);

CREATE TABLE symbols (
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    generation_id INTEGER NOT NULL,
    symbol_key TEXT NOT NULL,
    node_kind TEXT NOT NULL,
    package_key TEXT NOT NULL DEFAULT '',
    file_key TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    signature TEXT NOT NULL DEFAULT '',
    receiver TEXT NOT NULL DEFAULT '',
    start_line INTEGER NOT NULL DEFAULT 0 CHECK (start_line >= 0),
    end_line INTEGER NOT NULL DEFAULT 0 CHECK (end_line >= 0),
    exported INTEGER NOT NULL DEFAULT 0 CHECK (exported IN (0, 1)),
    PRIMARY KEY (repo_id, worktree_id, generation_id, symbol_key),
    FOREIGN KEY (repo_id, worktree_id, generation_id)
        REFERENCES graph_generations (repo_id, worktree_id, generation_id) ON DELETE CASCADE
);

CREATE TABLE nodes (
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    generation_id INTEGER NOT NULL,
    node_key TEXT NOT NULL,
    node_kind TEXT NOT NULL,
    owned INTEGER NOT NULL DEFAULT 1 CHECK (owned IN (0, 1)),
    package_key TEXT NOT NULL DEFAULT '',
    file_key TEXT NOT NULL DEFAULT '',
    symbol_key TEXT NOT NULL DEFAULT '',
    source_blob TEXT NOT NULL DEFAULT '',
    source_start INTEGER NOT NULL DEFAULT 0 CHECK (source_start >= 0),
    source_end INTEGER NOT NULL DEFAULT 0 CHECK (source_end >= 0),
    PRIMARY KEY (repo_id, worktree_id, generation_id, node_key),
    FOREIGN KEY (repo_id, worktree_id, generation_id)
        REFERENCES graph_generations (repo_id, worktree_id, generation_id) ON DELETE CASCADE
);

CREATE TABLE edges (
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    generation_id INTEGER NOT NULL,
    source_key TEXT NOT NULL,
    target_key TEXT NOT NULL,
    edge_kind TEXT NOT NULL,
    confidence TEXT NOT NULL DEFAULT 'exact',
    owner_package TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_id, worktree_id, generation_id, source_key, target_key, edge_kind),
    FOREIGN KEY (repo_id, worktree_id, generation_id, source_key)
        REFERENCES nodes (repo_id, worktree_id, generation_id, node_key) ON DELETE CASCADE,
    FOREIGN KEY (repo_id, worktree_id, generation_id, target_key)
        REFERENCES nodes (repo_id, worktree_id, generation_id, node_key) ON DELETE CASCADE
);

CREATE TABLE edge_evidence (
    evidence_id INTEGER PRIMARY KEY,
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    generation_id INTEGER NOT NULL,
    source_key TEXT NOT NULL,
    target_key TEXT NOT NULL,
    edge_kind TEXT NOT NULL,
    analyzer_source TEXT NOT NULL DEFAULT '',
    source_blob TEXT NOT NULL DEFAULT '',
    source_start INTEGER NOT NULL DEFAULT 0 CHECK (source_start >= 0),
    source_end INTEGER NOT NULL DEFAULT 0 CHECK (source_end >= 0),
    details TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (repo_id, worktree_id, generation_id, source_key, target_key, edge_kind)
        REFERENCES edges (repo_id, worktree_id, generation_id, source_key, target_key, edge_kind)
        ON DELETE CASCADE
);

CREATE TABLE package_dependencies (
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    generation_id INTEGER NOT NULL,
    source_package TEXT NOT NULL,
    target_package TEXT NOT NULL,
    PRIMARY KEY (repo_id, worktree_id, generation_id, source_package, target_package),
    FOREIGN KEY (repo_id, worktree_id, generation_id)
        REFERENCES graph_generations (repo_id, worktree_id, generation_id) ON DELETE CASCADE
);

CREATE TABLE test_relationships (
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    generation_id INTEGER NOT NULL,
    test_key TEXT NOT NULL,
    target_key TEXT NOT NULL,
    confidence TEXT NOT NULL DEFAULT 'inferred',
    PRIMARY KEY (repo_id, worktree_id, generation_id, test_key, target_key),
    FOREIGN KEY (repo_id, worktree_id, generation_id)
        REFERENCES graph_generations (repo_id, worktree_id, generation_id) ON DELETE CASCADE
);

CREATE TABLE parse_cache (
    repo_id TEXT NOT NULL,
    blob_sha TEXT NOT NULL,
    object_format TEXT NOT NULL DEFAULT 'sha1',
    parser_version TEXT NOT NULL,
    syntax_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (repo_id, blob_sha, object_format, parser_version),
    FOREIGN KEY (repo_id, blob_sha) REFERENCES blobs (repo_id, blob_sha) ON DELETE CASCADE
);

CREATE INDEX idx_worktrees_repo ON worktrees (repo_id);
CREATE INDEX idx_generations_state ON graph_generations (repo_id, worktree_id, state);
CREATE INDEX idx_nodes_kind ON nodes (repo_id, worktree_id, generation_id, node_kind);
CREATE INDEX idx_edges_source ON edges (repo_id, worktree_id, generation_id, source_key);
CREATE INDEX idx_edges_target ON edges (repo_id, worktree_id, generation_id, target_key);
CREATE INDEX idx_symbols_name ON symbols (repo_id, worktree_id, generation_id, name);
