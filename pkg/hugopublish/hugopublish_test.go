package hugopublish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// newFixtureSite creates a bare-bones Hugo site repo on disk (via go-git
// directly, no external git binary needed) with one nested section
// (content/fiction/story-a, two chapters + an _index.md) and returns its
// path plus the default branch name go-git's PlainInit uses.
func newFixtureSite(t *testing.T) (path string, branch string) {
	t.Helper()
	dir := t.TempDir()
	siteDir := filepath.Join(dir, "site")

	r, err := git.PlainInit(siteDir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}

	files := map[string]string{
		"hugo.yaml": "contentDir: content\n",
		"content/fiction/story-a/_index.md": "---\ntitle: Story A\ndraft: false\n---\n\n",
		"content/fiction/story-a/ch2.md":    "---\ntitle: Chapter Two\ndraft: false\nweight: 2\n---\n\nSecond chapter body.\n",
		"content/fiction/story-a/ch1.md":    "---\ntitle: Chapter One\ndraft: false\nweight: 1\n---\n\nFirst chapter body.\n",
	}

	w, err := r.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	for rel, content := range files {
		full := filepath.Join(siteDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if _, err := w.Add(rel); err != nil {
			t.Fatalf("add %s: %v", rel, err)
		}
	}
	_, err = w.Commit("seed fixture site", &git.CommitOptions{
		Author: &object.Signature{Name: "Fixture", Email: "fixture@localhost", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Real Hugo remotes (GitHub etc.) are bare, so pushing to their checked
	// out branch is a non-issue. This fixture is a normal working repo
	// standing in for that remote, so it needs receive.denyCurrentBranch
	// relaxed or a push to its checked-out branch is rejected — a fixture
	// artifact, not something Publish's caller needs to worry about.
	cfg, err := r.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.Raw.Section("receive").SetOption("denyCurrentBranch", "updateInstead")
	if err := r.SetConfig(cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	return siteDir, "master" // go-git's PlainInit default branch
}

func TestProbeClassifiesNestedSection(t *testing.T) {
	Init(t.TempDir())
	siteDir, branch := newFixtureSite(t)

	result, err := Probe(ProbeRequest{RepoURL: "file://" + siteDir, Branch: branch})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if result.ContentDir != "content" || result.ContentDirGuessed {
		t.Fatalf("expected explicit contentDir=content, got %+v", result)
	}
	if len(result.Sections) != 1 {
		t.Fatalf("expected 1 section, got %d: %+v", len(result.Sections), result.Sections)
	}
	sec := result.Sections[0]
	if sec.Layout != LayoutNested {
		t.Fatalf("expected nested layout, got %s", sec.Layout)
	}
	if len(sec.Stories) != 1 || sec.Stories[0].Slug != "story-a" || sec.Stories[0].ChapterCount != 2 {
		t.Fatalf("unexpected stories: %+v", sec.Stories)
	}
	if sec.Stories[0].Title != "Story A" {
		t.Fatalf("expected story title from _index.md, got %q", sec.Stories[0].Title)
	}
}

func TestImportStoryOrdersByWeight(t *testing.T) {
	Init(t.TempDir())
	siteDir, branch := newFixtureSite(t)

	result, err := ImportStory(ImportRequest{
		RepoURL:   "file://" + siteDir,
		Branch:    branch,
		StoryPath: "content/fiction/story-a",
		Layout:    LayoutNested,
	})
	if err != nil {
		t.Fatalf("ImportStory: %v", err)
	}
	if result.StoryTitle != "Story A" {
		t.Fatalf("expected story title Story A, got %q", result.StoryTitle)
	}
	if len(result.Chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(result.Chapters))
	}
	if result.Chapters[0].Title != "Chapter One" || result.Chapters[1].Title != "Chapter Two" {
		t.Fatalf("expected weight-ordered chapters, got %q then %q", result.Chapters[0].Title, result.Chapters[1].Title)
	}
}

func TestPublishCommitsAndPushes(t *testing.T) {
	Init(t.TempDir())
	siteDir, branch := newFixtureSite(t)

	weight := 3
	result, err := Publish(PublishRequest{
		RepoURL: "file://" + siteDir,
		Branch:  branch,
		Files: []FileWrite{{
			Path:        "content/fiction/story-a/ch3.md",
			FrontMatter: FrontMatter{Title: "Chapter Three", Weight: &weight},
			Body:        "Third chapter body.\n",
		}},
		CommitMessage: "Publish Chapter Three",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if result.CommitSHA == "" || len(result.FilesWritten) != 1 {
		t.Fatalf("unexpected publish result: %+v", result)
	}

	// Verify directly against the origin repo, independent of our own clone
	// cache, that the push actually landed.
	r, err := git.PlainOpen(siteDir)
	if err != nil {
		t.Fatalf("PlainOpen origin: %v", err)
	}
	head, err := r.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head.Hash().String() != result.CommitSHA {
		t.Fatalf("origin HEAD %s does not match reported commit %s", head.Hash(), result.CommitSHA)
	}

	written, err := os.ReadFile(filepath.Join(siteDir, "content/fiction/story-a/ch3.md"))
	if err != nil {
		t.Fatalf("reading published file: %v", err)
	}
	if !strings.Contains(string(written), "title: Chapter Three") || !strings.Contains(string(written), "Third chapter body.") {
		t.Fatalf("published file missing expected content:\n%s", written)
	}
}
