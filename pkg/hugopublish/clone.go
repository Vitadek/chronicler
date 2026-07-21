package hugopublish

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// cacheRoot is set once by Init and used to derive per-repo clone dirs.
var (
	cacheRoot string

	repoLocksMu sync.Mutex
	repoLocks   = map[string]*sync.Mutex{}
)

// Init wires the package to a data directory. Call once at startup, before
// any Probe/Publish/ImportStory call — mirrors how other pkg/* packages take
// their storage root from config.Config.DataDir.
func Init(dataDir string) {
	cacheRoot = filepath.Join(dataDir, "hugo-publish-cache")
}

func repoCacheDir(repoURL string) string {
	sum := sha256.Sum256([]byte(repoURL))
	return filepath.Join(cacheRoot, hex.EncodeToString(sum[:])[:16])
}

// lockFor returns the process-local mutex guarding a given repo's clone
// directory, serializing concurrent requests against the same repo so two
// publishes can't race on the same worktree. This is process-local only —
// it does not protect against multiple Chronicler replicas touching the
// same repo concurrently; that would need a distributed lock (e.g. an
// SQLite row lock), which is out of scope here.
func lockFor(dir string) *sync.Mutex {
	repoLocksMu.Lock()
	defer repoLocksMu.Unlock()
	l, ok := repoLocks[dir]
	if !ok {
		l = &sync.Mutex{}
		repoLocks[dir] = l
	}
	return l
}

func authFor(token string) *http.BasicAuth {
	if token == "" {
		return nil
	}
	return &http.BasicAuth{Username: "x-access-token", Password: token}
}

// EnsureClone returns a writable worktree directory checked out to branch,
// cloning fresh or fetching+hard-resetting an existing cache dir. The caller
// must call unlock() (typically via defer) once done with the directory.
func EnsureClone(repoURL, branch, token string) (dir string, unlock func(), err error) {
	if cacheRoot == "" {
		return "", nil, fmt.Errorf("hugopublish: Init(dataDir) was never called")
	}
	dir = repoCacheDir(repoURL)
	lock := lockFor(dir)
	lock.Lock()
	unlock = lock.Unlock

	if _, statErr := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(statErr) {
		if mkErr := os.MkdirAll(filepath.Dir(dir), 0755); mkErr != nil {
			unlock()
			return "", nil, mkErr
		}
		_, cloneErr := git.PlainClone(dir, false, &git.CloneOptions{
			URL:  repoURL,
			Auth: authFor(token),
		})
		if cloneErr != nil {
			os.RemoveAll(dir)
			unlock()
			return "", nil, fmt.Errorf("clone failed: %w", cloneErr)
		}
	} else {
		r, openErr := git.PlainOpen(dir)
		if openErr != nil {
			unlock()
			return "", nil, openErr
		}
		fetchErr := r.Fetch(&git.FetchOptions{RemoteName: "origin", Auth: authFor(token), Force: true})
		if fetchErr != nil && fetchErr != git.NoErrAlreadyUpToDate {
			unlock()
			return "", nil, fmt.Errorf("fetch failed: %w", fetchErr)
		}
	}

	if resetErr := checkoutBranch(dir, branch, false); resetErr != nil {
		unlock()
		return "", nil, resetErr
	}

	return dir, unlock, nil
}

// checkoutBranch hard-resets the worktree to origin/<branch>. If the branch
// doesn't exist remotely and create is true, it's created from the current
// HEAD instead (used by Publish when CreateBranchIfMissing is set).
func checkoutBranch(dir, branch string, create bool) error {
	r, err := git.PlainOpen(dir)
	if err != nil {
		return err
	}
	w, err := r.Worktree()
	if err != nil {
		return err
	}

	remoteRefName := plumbing.NewRemoteReferenceName("origin", branch)
	remoteRef, refErr := r.Reference(remoteRefName, true)
	if refErr != nil {
		if !create {
			return fmt.Errorf("branch %q not found on origin: %w", branch, refErr)
		}
		// Branch doesn't exist remotely yet — check out a new local branch
		// from whatever HEAD currently points at.
		localRefName := plumbing.NewBranchReferenceName(branch)
		return w.Checkout(&git.CheckoutOptions{Branch: localRefName, Create: true})
	}

	localRefName := plumbing.NewBranchReferenceName(branch)
	localRef := plumbing.NewHashReference(localRefName, remoteRef.Hash())
	if err := r.Storer.SetReference(localRef); err != nil {
		return err
	}
	if err := w.Checkout(&git.CheckoutOptions{Branch: localRefName, Force: true}); err != nil {
		return err
	}
	return w.Reset(&git.ResetOptions{Commit: remoteRef.Hash(), Mode: git.HardReset})
}
