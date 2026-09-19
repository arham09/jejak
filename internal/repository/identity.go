// Package repository defines stable repository and worktree identities.
package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/arham09/jejak/internal/git"
)

// RepoID is the stable storage key for one logical repository.
type RepoID string

// WorktreeID is the stable storage key for one local worktree.
type WorktreeID string

// CommitSHA identifies a Git commit. An empty value represents an unborn
// repository or an unindexed state.
type CommitSHA string

// ErrAmbiguousRemote indicates that a repository has multiple possible
// canonical remotes and no origin remote resolves the ambiguity.
var ErrAmbiguousRemote = errors.New("ambiguous Git remote identity")

// Descriptor contains the durable identity and source paths of one logical
// repository.
type Descriptor struct {
	ID                RepoID
	CanonicalIdentity string
	Root              string
	CommonDir         string
	RemoteName        string
	RemoteURL         string
}

// Worktree contains local state that must remain independent between linked
// worktrees and independent clones.
type Worktree struct {
	ID        WorktreeID
	Path      string
	GitDir    string
	CommonDir string
	Branch    string
	Head      CommitSHA
	HeadKnown bool
}

// Target is a fully resolved repository/worktree pair for one command.
type Target struct {
	Repository Descriptor
	Worktree   Worktree
}

// Identify converts neutral Git metadata into stable repository and worktree
// identities. Origin is preferred; a remote-less repository falls back to the
// canonical Git common directory.
func Identify(info git.RepositoryInfo) (Target, error) {
	root, err := canonicalPath(info.Root)
	if err != nil {
		return Target{}, fmt.Errorf("canonicalize repository root: %w", err)
	}
	commonDir, err := canonicalPath(info.CommonDir)
	if err != nil {
		return Target{}, fmt.Errorf("canonicalize Git common directory: %w", err)
	}
	gitDir, err := canonicalPath(info.GitDir)
	if err != nil {
		return Target{}, fmt.Errorf("canonicalize Git directory: %w", err)
	}

	remote, hasRemote, err := selectRemote(info.Remotes)
	if err != nil {
		return Target{}, err
	}
	identity := "local:" + commonDir
	if hasRemote {
		identity = remote.canonical
	}
	repoID := RepoID(hashID("repo", identity))
	worktreeMaterial := strings.Join([]string{commonDir, gitDir, root}, "\x00")
	worktreeID := WorktreeID(hashID("worktree", worktreeMaterial))

	return Target{
		Repository: Descriptor{
			ID:                repoID,
			CanonicalIdentity: identity,
			Root:              root,
			CommonDir:         commonDir,
			RemoteName:        remote.name,
			RemoteURL:         remote.url,
		},
		Worktree: Worktree{
			ID:        worktreeID,
			Path:      root,
			GitDir:    gitDir,
			CommonDir: commonDir,
			Branch:    info.Branch,
			Head:      CommitSHA(info.Head),
			HeadKnown: info.HeadKnown,
		},
	}, nil
}

// CanonicalRemote normalizes transport and authentication details while
// preserving the remote host port and case-sensitive repository path.
func CanonicalRemote(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("remote URL is empty")
	}

	if host, path, ok := scpRemote(raw); ok {
		if strings.Trim(strings.TrimSuffix(path, ".git"), "/") == "" {
			return "", fmt.Errorf("remote URL %q has no repository path", raw)
		}
		return normalizeHostPath(host, path), nil
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		path := strings.Trim(parsed.Path, "/")
		path = strings.TrimSuffix(path, ".git")
		if path == "" {
			return "", fmt.Errorf("remote URL %q has no repository path", raw)
		}
		if parsed.Scheme == "file" {
			if parsed.Host == "" {
				return "file:" + "/" + path, nil
			}
			return "file://" + strings.ToLower(parsed.Host) + "/" + path, nil
		}
		host := strings.ToLower(parsed.Hostname())
		if host == "" {
			return "", fmt.Errorf("remote URL %q has no host", raw)
		}
		if port := parsed.Port(); port != "" {
			host += ":" + port
		}
		return normalizeHostPath(host, path), nil
	}

	value := strings.Trim(strings.TrimSuffix(raw, ".git"), "/")
	value = strings.TrimPrefix(value, "/")
	if value == "" {
		return "", fmt.Errorf("remote URL %q has no identity", raw)
	}
	return value, nil
}

type selectedRemote struct {
	name      string
	url       string
	canonical string
}

func selectRemote(remotes []git.Remote) (selectedRemote, bool, error) {
	if len(remotes) == 0 {
		return selectedRemote{}, false, nil
	}
	preferred := make([]git.Remote, 0, len(remotes))
	for _, remote := range remotes {
		if remote.Name == "origin" {
			preferred = append(preferred, remote)
		}
	}
	candidates := remotes
	if len(preferred) > 0 {
		candidates = preferred
	}

	var selected selectedRemote
	seen := make(map[string]struct{}, len(candidates))
	for _, remote := range candidates {
		canonical, err := CanonicalRemote(remote.URL)
		if err != nil {
			return selectedRemote{}, false, fmt.Errorf("canonicalize remote %q: %w", remote.Name, err)
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		if selected.canonical != "" {
			return selectedRemote{}, false, fmt.Errorf("%w: %s and %s", ErrAmbiguousRemote, selected.canonical, canonical)
		}
		selected = selectedRemote{name: remote.Name, url: remote.URL, canonical: canonical}
	}
	return selected, selected.canonical != "", nil
}

func scpRemote(raw string) (host, path string, ok bool) {
	if strings.Contains(raw, "://") {
		return "", "", false
	}
	colon := strings.IndexByte(raw, ':')
	if colon <= 0 || strings.Contains(raw[:colon], "/") {
		return "", "", false
	}
	left := raw[:colon]
	if at := strings.LastIndexByte(left, '@'); at >= 0 {
		host = left[at+1:]
	} else {
		host = left
	}
	if host == "" || strings.HasPrefix(raw, "file:") {
		return "", "", false
	}
	return host, raw[colon+1:], true
}

func normalizeHostPath(host, path string) string {
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	return strings.ToLower(host) + "/" + path
}

func hashID(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + "-" + hex.EncodeToString(sum[:])
}

func canonicalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	return abs, nil
}
