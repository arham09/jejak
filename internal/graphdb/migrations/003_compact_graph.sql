-- Compact generation-scoped storage.
--
-- Every graph row used to repeat the repository and worktree identifiers
-- (about 140 bytes) and every edge repeated two long text node keys, in the
-- table and again in each index. One generation of a mid-size Go service cost
-- hundreds of megabytes, and nothing removed superseded generations, so a
-- store grew without bound. Generation-scoped tables now hang off one integer
-- generation key, edges reference nodes by integer id, and evidence
-- references its edge by integer id.
--
-- Stored generations are dropped rather than converted: the record format
-- changes shape, so the graph manager rebuilds the committed graph on the
-- next init or sync anyway. Dropping whole tables also releases a bloated
-- store without deleting rows one by one. Repository, worktree, blob, and
-- parse-cache records keep their layout.
UPDATE graph_state
SET active_generation = NULL,
    indexed_head = '',
    status = 'unindexed',
    last_error = '',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

DROP TABLE edge_evidence;
DROP TABLE edges;
DROP TABLE test_relationships;
DROP TABLE package_dependencies;
DROP TABLE nodes;
DROP TABLE symbols;
DROP TABLE files;
DROP TABLE packages;
DROP TABLE graph_generations;

CREATE TABLE graph_generations (
    generation_key INTEGER PRIMARY KEY,
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    generation_id INTEGER NOT NULL CHECK (generation_id > 0),
    commit_sha TEXT NOT NULL DEFAULT '',
    build_fingerprint TEXT NOT NULL DEFAULT '',
    analyzer_version TEXT NOT NULL DEFAULT '',
    schema_version INTEGER NOT NULL DEFAULT 2 CHECK (schema_version > 0),
    state TEXT NOT NULL CHECK (state IN ('building', 'validated', 'active', 'retired', 'superseded', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    validated_at TEXT,
    retired_at TEXT,
    UNIQUE (repo_id, worktree_id, generation_id),
    FOREIGN KEY (repo_id, worktree_id) REFERENCES worktrees (repo_id, worktree_id) ON DELETE CASCADE
);

CREATE INDEX idx_generations_state ON graph_generations (repo_id, worktree_id, state);

CREATE TABLE packages (
    generation_key INTEGER NOT NULL REFERENCES graph_generations (generation_key) ON DELETE CASCADE,
    package_key TEXT NOT NULL,
    import_path TEXT NOT NULL DEFAULT '',
    module_path TEXT NOT NULL DEFAULT '',
    directory TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (generation_key, package_key)
) WITHOUT ROWID;

CREATE TABLE files (
    generation_key INTEGER NOT NULL REFERENCES graph_generations (generation_key) ON DELETE CASCADE,
    file_key TEXT NOT NULL,
    path TEXT NOT NULL,
    blob_sha TEXT NOT NULL DEFAULT '',
    package_key TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (generation_key, file_key),
    UNIQUE (generation_key, path)
) WITHOUT ROWID;

CREATE TABLE symbols (
    symbol_id INTEGER PRIMARY KEY,
    generation_key INTEGER NOT NULL REFERENCES graph_generations (generation_key) ON DELETE CASCADE,
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
    UNIQUE (generation_key, symbol_key)
);

CREATE INDEX idx_symbols_name ON symbols (generation_key, name);
CREATE INDEX idx_symbols_file ON symbols (generation_key, file_key);

-- Edges reference nodes by node_id without a declared foreign key. A foreign
-- key would make SQLite check edges by bare node_id on every node delete,
-- which no index serves; generations are deleted as a whole through
-- generation_key instead, and doctor verifies endpoint integrity.
CREATE TABLE nodes (
    node_id INTEGER PRIMARY KEY,
    generation_key INTEGER NOT NULL REFERENCES graph_generations (generation_key) ON DELETE CASCADE,
    node_key TEXT NOT NULL,
    node_kind TEXT NOT NULL,
    owned INTEGER NOT NULL DEFAULT 1 CHECK (owned IN (0, 1)),
    package_key TEXT NOT NULL DEFAULT '',
    file_key TEXT NOT NULL DEFAULT '',
    symbol_key TEXT NOT NULL DEFAULT '',
    source_blob TEXT NOT NULL DEFAULT '',
    source_start INTEGER NOT NULL DEFAULT 0 CHECK (source_start >= 0),
    source_end INTEGER NOT NULL DEFAULT 0 CHECK (source_end >= 0),
    UNIQUE (generation_key, node_key)
);

CREATE INDEX idx_nodes_kind ON nodes (generation_key, node_kind);

CREATE TABLE edges (
    edge_id INTEGER PRIMARY KEY,
    generation_key INTEGER NOT NULL REFERENCES graph_generations (generation_key) ON DELETE CASCADE,
    source_id INTEGER NOT NULL,
    target_id INTEGER NOT NULL,
    edge_kind TEXT NOT NULL,
    confidence TEXT NOT NULL DEFAULT 'exact',
    owner_package TEXT NOT NULL DEFAULT '',
    UNIQUE (generation_key, source_id, target_id, edge_kind)
);

CREATE INDEX idx_edges_target ON edges (generation_key, target_id, edge_kind);

CREATE TABLE edge_evidence (
    evidence_id INTEGER PRIMARY KEY,
    edge_id INTEGER NOT NULL REFERENCES edges (edge_id) ON DELETE CASCADE,
    analyzer_source TEXT NOT NULL DEFAULT '',
    source_blob TEXT NOT NULL DEFAULT '',
    source_start INTEGER NOT NULL DEFAULT 0 CHECK (source_start >= 0),
    source_end INTEGER NOT NULL DEFAULT 0 CHECK (source_end >= 0),
    details TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_edge_evidence_edge ON edge_evidence (edge_id);

CREATE TABLE package_dependencies (
    generation_key INTEGER NOT NULL REFERENCES graph_generations (generation_key) ON DELETE CASCADE,
    source_package TEXT NOT NULL,
    target_package TEXT NOT NULL,
    PRIMARY KEY (generation_key, source_package, target_package)
) WITHOUT ROWID;

CREATE TABLE test_relationships (
    generation_key INTEGER NOT NULL REFERENCES graph_generations (generation_key) ON DELETE CASCADE,
    test_key TEXT NOT NULL,
    target_key TEXT NOT NULL,
    confidence TEXT NOT NULL DEFAULT 'inferred',
    PRIMARY KEY (generation_key, test_key, target_key)
) WITHOUT ROWID;
