package repository

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arham09/jejak/internal/git"
)

func TestCanonicalRemote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "scp", in: "git@github.com:company/payment-service.git", want: "github.com/company/payment-service"},
		{name: "https credentials", in: "https://user:secret@GitHub.com/company/payment-service.git", want: "github.com/company/payment-service"},
		{name: "ssh port", in: "ssh://git@GitHub.com:2222/Company/Payment.git", want: "github.com:2222/Company/Payment"},
		{name: "path case", in: "https://github.com/Company/Payment.git/", want: "github.com/Company/Payment"},
		{name: "scp trailing slash", in: "git@github.com:Company/Payment.git/", want: "github.com/Company/Payment"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalRemote(tt.in)
			if err != nil {
				t.Fatalf("CanonicalRemote() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("CanonicalRemote() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIdentifyRemoteSelectionAndWorktreeIdentity(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	gitDir := filepath.Join(common, "worktrees", "feature")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	info := git.RepositoryInfo{
		Root:      root,
		GitDir:    gitDir,
		CommonDir: common,
		Head:      "abc123",
		HeadKnown: true,
		Branch:    "feature/test",
		Remotes: []git.Remote{
			{Name: "upstream", URL: "https://github.com/company/other.git"},
			{Name: "origin", URL: "git@github.com:company/payment-service.git"},
		},
	}
	target, err := Identify(info)
	if err != nil {
		t.Fatalf("Identify() error = %v", err)
	}
	if target.Repository.CanonicalIdentity != "github.com/company/payment-service" {
		t.Fatalf("identity = %q", target.Repository.CanonicalIdentity)
	}
	if target.Worktree.Branch != info.Branch || !target.Worktree.HeadKnown {
		t.Fatalf("worktree metadata = %#v", target.Worktree)
	}
	again, err := Identify(info)
	if err != nil {
		t.Fatal(err)
	}
	if target.Repository.ID != again.Repository.ID || target.Worktree.ID != again.Worktree.ID {
		t.Fatal("same Git metadata produced unstable identity")
	}
	other := info
	other.GitDir = filepath.Join(common, "worktrees", "other")
	if err := os.MkdirAll(other.GitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	otherTarget, err := Identify(other)
	if err != nil {
		t.Fatal(err)
	}
	if target.Repository.ID != otherTarget.Repository.ID {
		t.Fatal("linked worktrees should share repository identity")
	}
	if target.Worktree.ID == otherTarget.Worktree.ID {
		t.Fatal("linked worktrees should have independent worktree identity")
	}
}

func TestIdentifyAmbiguousRemoteWithoutOrigin(t *testing.T) {
	root := t.TempDir()
	info := git.RepositoryInfo{Root: root, GitDir: root, CommonDir: root, Remotes: []git.Remote{
		{Name: "upstream", URL: "https://github.com/company/one.git"},
		{Name: "mirror", URL: "https://github.com/company/two.git"},
	}}
	_, err := Identify(info)
	if !errors.Is(err, ErrAmbiguousRemote) {
		t.Fatalf("Identify() error = %v, want ErrAmbiguousRemote", err)
	}
}

func TestIdentifyRemoteLessUsesCommonDirectory(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	if err := os.MkdirAll(common, 0o755); err != nil {
		t.Fatal(err)
	}
	info := git.RepositoryInfo{Root: root, GitDir: common, CommonDir: common}
	target, err := Identify(info)
	if err != nil {
		t.Fatal(err)
	}
	if target.Repository.RemoteName != "" || target.Repository.RemoteURL != "" {
		t.Fatalf("remote-less target has remote %#v", target.Repository)
	}
	if target.Repository.CanonicalIdentity[:len("local:")] != "local:" {
		t.Fatalf("fallback identity = %q", target.Repository.CanonicalIdentity)
	}
}
