package plugins

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func commitTestFile(t *testing.T, repoDir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("test update", &git.CommitOptions{Author: &object.Signature{
		Name: "Chronicle Test", Email: "test@chronicle.invalid", When: time.Unix(1, 0),
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestPullRepoPreservesChronicleMetadata(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	pluginDir := filepath.Join(pluginsDir, "chronicle.proofreader")
	repo, err := git.PlainInit(pluginDir, false)
	if err != nil {
		t.Fatal(err)
	}
	commitTestFile(t, pluginDir, "one")
	commitTestFile(t, pluginDir, "two")
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	gitURL := "https://forgejo.lan/protoman/chronicle-plugin-proofreader.git"
	wantMeta := RepoMeta{GitUrl: &gitURL, Source: "git"}
	if err := WriteMeta(pluginDir, wantMeta); err != nil {
		t.Fatal(err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := hardResetPreservingMeta(worktree, pluginDir, head.Hash(), wantMeta); err != nil {
		t.Fatal(err)
	}

	got := ReadMeta(pluginDir)
	if got.Source != "git" || got.GitUrl == nil || *got.GitUrl != gitURL {
		t.Fatalf("metadata lost during pull: %#v", got)
	}
}
