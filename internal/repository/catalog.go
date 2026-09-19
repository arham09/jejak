package repository

import "time"

// CatalogEntry is the metadata shown by repository listing commands. It does
// not contain semantic graph records.
type CatalogEntry struct {
	ID                RepoID
	CanonicalIdentity string
	Root              string
	CommonDir         string
	RemoteName        string
	RemoteURL         string
	WorktreeCount     int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
