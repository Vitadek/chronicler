package hugopublish

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// ErrNonFastForward is returned when the remote moved since the caller last
// probed it — the frontend should surface this as "repo changed, reload and
// retry" rather than a generic failure.
var ErrNonFastForward = errors.New("hugopublish: remote branch moved since last probe — reload and retry")

// Publish clones/checks out req.Branch (creating it from HEAD if it doesn't
// exist and CreateBranchIfMissing is set), writes every file in req.Files,
// makes one batched commit, and pushes. The token is used only for this
// call's clone/fetch/push auth — it is never written to disk or logged.
func Publish(req PublishRequest) (*PublishResult, error) {
	if len(req.Files) == 0 {
		return nil, fmt.Errorf("hugopublish: no files to publish")
	}

	dir, unlock, err := ensureCloneForPublish(req)
	if err != nil {
		return nil, err
	}
	defer unlock()

	r, err := git.PlainOpen(dir)
	if err != nil {
		return nil, err
	}
	w, err := r.Worktree()
	if err != nil {
		return nil, err
	}

	written := make([]string, 0, len(req.Files))
	for _, f := range req.Files {
		content, buildErr := BuildFrontMatter(f.FrontMatter, f.Body)
		if buildErr != nil {
			return nil, buildErr
		}
		fullPath := filepath.Join(dir, filepath.FromSlash(f.Path))
		if mkErr := os.MkdirAll(filepath.Dir(fullPath), 0755); mkErr != nil {
			return nil, mkErr
		}
		if writeErr := os.WriteFile(fullPath, []byte(content), 0644); writeErr != nil {
			return nil, writeErr
		}
		if _, addErr := w.Add(filepath.ToSlash(f.Path)); addErr != nil {
			return nil, fmt.Errorf("staging %s: %w", f.Path, addErr)
		}
		written = append(written, f.Path)
	}

	name := req.CommitterName
	if name == "" {
		name = "Chronicler"
	}
	email := req.CommitterEmail
	if email == "" {
		email = "chronicler@localhost"
	}
	msg := req.CommitMessage
	if msg == "" {
		msg = fmt.Sprintf("Publish %d file(s) via Chronicler", len(written))
	}

	commitHash, err := w.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: name, Email: email, When: time.Now()},
	})
	if err != nil {
		return nil, fmt.Errorf("commit failed: %w", err)
	}

	pushErr := r.Push(&git.PushOptions{RemoteName: "origin", Auth: authFor(req.Token)})
	if pushErr != nil {
		if errors.Is(pushErr, git.NoErrAlreadyUpToDate) {
			// Nothing to push (shouldn't happen right after a commit, but
			// treat as success defensively).
		} else if isNonFastForward(pushErr) {
			return nil, ErrNonFastForward
		} else {
			return nil, fmt.Errorf("push failed: %w", pushErr)
		}
	}

	return &PublishResult{CommitSHA: commitHash.String(), Branch: req.Branch, FilesWritten: written}, nil
}

func ensureCloneForPublish(req PublishRequest) (string, func(), error) {
	dir, unlock, err := EnsureClone(req.RepoURL, req.Branch, req.Token)
	if err == nil {
		return dir, unlock, nil
	}
	if !req.CreateBranchIfMissing {
		return "", nil, err
	}
	// Branch likely doesn't exist remotely yet. Clone the repo's default
	// branch instead, then create req.Branch locally from that HEAD.
	dir = repoCacheDir(req.RepoURL)
	lock := lockFor(dir)
	lock.Lock()
	unlockFn := lock.Unlock

	if _, statErr := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(statErr) {
		if mkErr := os.MkdirAll(filepath.Dir(dir), 0755); mkErr != nil {
			unlockFn()
			return "", nil, mkErr
		}
		if _, cloneErr := git.PlainClone(dir, false, &git.CloneOptions{URL: req.RepoURL, Auth: authFor(req.Token)}); cloneErr != nil {
			os.RemoveAll(dir)
			unlockFn()
			return "", nil, fmt.Errorf("clone failed: %w", cloneErr)
		}
	}
	if checkoutErr := checkoutBranch(dir, req.Branch, true); checkoutErr != nil {
		unlockFn()
		return "", nil, checkoutErr
	}
	return dir, unlockFn, nil
}

func isNonFastForward(err error) bool {
	return err != nil && (errors.Is(err, git.ErrNonFastForwardUpdate) || strings.Contains(err.Error(), "non-fast-forward"))
}
