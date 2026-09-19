package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

const manifestVersion = 1

type manifest struct {
	Version      int                      `json:"version"`
	RepositoryID string                   `json:"repository_id"`
	Entries      map[string]manifestEntry `json:"entries"`
}

type manifestEntry struct {
	Name            string   `json:"name"`
	Path            string   `json:"path"`
	BackupPath      string   `json:"backup_path,omitempty"`
	OriginalExists  bool     `json:"original_exists"`
	OriginalMode    uint32   `json:"original_mode,omitempty"`
	OriginalSHA256  string   `json:"original_sha256,omitempty"`
	InstalledMode   uint32   `json:"installed_mode"`
	InstalledSHA256 string   `json:"installed_sha256"`
	Owners          []string `json:"owners"`
}

func readManifest(path, repositoryID string) (manifest, error) {
	document := manifest{Version: manifestVersion, RepositoryID: repositoryID, Entries: make(map[string]manifestEntry)}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return manifest{}, fmt.Errorf("read hook manifest %q: %w", path, err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return manifest{}, fmt.Errorf("parse hook manifest %q: %w", path, err)
	}
	if document.Version != manifestVersion {
		return manifest{}, fmt.Errorf("unsupported hook manifest version %d", document.Version)
	}
	if document.RepositoryID != "" && document.RepositoryID != repositoryID {
		return manifest{}, fmt.Errorf("hook manifest belongs to repository %q", document.RepositoryID)
	}
	document.RepositoryID = repositoryID
	if document.Entries == nil {
		document.Entries = make(map[string]manifestEntry)
	}
	for key, entry := range document.Entries {
		if entry.Name == "" || entry.Path == "" || entry.InstalledSHA256 == "" {
			return manifest{}, fmt.Errorf("hook manifest entry %q is incomplete", key)
		}
		if key != manifestKey(entry.Name, entry.Path) {
			return manifest{}, fmt.Errorf("hook manifest entry %q has an inconsistent key", key)
		}
		entry.Owners = normalizeOwners(entry.Owners)
		document.Entries[key] = entry
	}
	return document, nil
}

func writeManifest(path string, document manifest) error {
	if len(document.Entries) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove empty hook manifest: %w", err)
		}
		return nil
	}
	document.Version = manifestVersion
	document.Entries = normalizeEntries(document.Entries)
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode hook manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create hook manifest directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("protect hook manifest directory: %w", err)
	}
	return writeAtomic(path, data, 0o600)
}

func normalizeEntries(entries map[string]manifestEntry) map[string]manifestEntry {
	result := make(map[string]manifestEntry, len(entries))
	for key, entry := range entries {
		entry.Owners = normalizeOwners(entry.Owners)
		result[key] = entry
	}
	return result
}

func manifestKey(name, path string) string { return name + "\x00" + path }

func addOwner(entry *manifestEntry, owner string) bool {
	for _, existing := range entry.Owners {
		if existing == owner {
			return false
		}
	}
	entry.Owners = normalizeOwners(append(entry.Owners, owner))
	return true
}

func removeOwner(entry *manifestEntry, owner string) bool {
	if len(entry.Owners) == 0 {
		return true
	}
	result := make([]string, 0, len(entry.Owners))
	removed := false
	for _, existing := range entry.Owners {
		if existing == owner {
			removed = true
			continue
		}
		result = append(result, existing)
	}
	entry.Owners = result
	return removed
}

func normalizeOwners(owners []string) []string {
	if len(owners) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(owners))
	result := make([]string, 0, len(owners))
	for _, owner := range owners {
		if owner == "" {
			continue
		}
		if _, exists := seen[owner]; exists {
			continue
		}
		seen[owner] = struct{}{}
		result = append(result, owner)
	}
	sort.Strings(result)
	return result
}
