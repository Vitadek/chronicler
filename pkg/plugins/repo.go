package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

var PluginIDRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type RepoMeta struct {
	GitUrl    *string `json:"gitUrl"`
	PinnedRef *string `json:"pinnedRef"`
	Source    string  `json:"source"` // "seed" | "git" | "local"
}

type IncomingCommit struct {
	Oid     string `json:"oid"`
	Message string `json:"message"`
}

type UpdateStatus struct {
	UpdateAvailable bool             `json:"updateAvailable"`
	Incoming        []IncomingCommit `json:"incoming"`
}

func emptyUpdateStatus() *UpdateStatus {
	return &UpdateStatus{Incoming: make([]IncomingCommit, 0)}
}

type DiskPlugin struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Version      string            `json:"version"`
	Source       string            `json:"source"`
	GitUrl       *string           `json:"gitUrl"`
	Commit       *string           `json:"commit"`
	PinnedRef    *string           `json:"pinnedRef"`
	BuildError   *string           `json:"buildError"`
	Provides     []string          `json:"provides"`
	Requires     []string          `json:"requires"`
	Wants        []string          `json:"wants"`
	Conflicts    []string          `json:"conflicts"`
	Replaces     []string          `json:"replaces"`
	Dependencies map[string]string `json:"dependencies"`
}

func ValidatePluginID(id string) bool {
	return PluginIDRegexp.MatchString(strings.ToLower(id))
}

func PluginDir(pluginsDir string, id string) (string, error) {
	if !ValidatePluginID(id) {
		return "", fmt.Errorf("invalid plugin id: %s", id)
	}

	dir := filepath.Join(pluginsDir, id)
	resolvedDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	resolvedBase, err := filepath.Abs(pluginsDir)
	if err != nil {
		return "", err
	}

	if !strings.HasPrefix(resolvedDir, resolvedBase+string(filepath.Separator)) && resolvedDir != resolvedBase {
		return "", fmt.Errorf("plugin id escapes plugins directory: %s", id)
	}

	return resolvedDir, nil
}

func metaPath(dir string) string {
	return filepath.Join(dir, ".chronicle-meta.json")
}

func ReadMeta(dir string) RepoMeta {
	data, err := os.ReadFile(metaPath(dir))
	if err != nil {
		return RepoMeta{Source: "local"}
	}
	var meta RepoMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return RepoMeta{Source: "local"}
	}
	return meta
}

func WriteMeta(dir string, meta RepoMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath(dir), data, 0644)
}

func CurrentCommit(dir string) (*string, error) {
	full, err := CurrentCommitFull(dir)
	if err != nil {
		return nil, err
	}
	if full == nil {
		return nil, nil
	}
	short := (*full)[:7]
	return &short, nil
}

func CurrentCommitFull(dir string) (*string, error) {
	r, err := git.PlainOpen(dir)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return nil, nil
		}
		return nil, err
	}

	head, err := r.Head()
	if err != nil {
		return nil, err
	}

	commitHash := head.Hash().String()
	return &commitHash, nil
}

func CloneRepo(pluginsDir string, id string, url string) error {
	dir, err := PluginDir(pluginsDir, id)
	if err != nil {
		return err
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		return fmt.Errorf("plugin %q is already installed", id)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	_, err = git.PlainClone(dir, false, &git.CloneOptions{
		URL:          url,
		Depth:        50,
		SingleBranch: true,
	})
	if err != nil {
		os.RemoveAll(dir) // Cleanup half-clone
		return err
	}

	return WriteMeta(dir, RepoMeta{
		GitUrl:    &url,
		PinnedRef: nil,
		Source:    "git",
	})
}

func CheckForUpdates(pluginsDir string, id string) (*UpdateStatus, error) {
	dir, err := PluginDir(pluginsDir, id)
	if err != nil {
		return nil, err
	}

	meta := ReadMeta(dir)
	if meta.Source != "git" || meta.PinnedRef != nil {
		return emptyUpdateStatus(), nil
	}

	r, err := git.PlainOpen(dir)
	if err != nil {
		return nil, err
	}

	err = r.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		Force:      true,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return nil, err
	}

	head, err := r.Head()
	if err != nil {
		return nil, err
	}
	headHash := head.Hash()

	branchName := head.Name().Short()
	remoteRefName := plumbing.NewRemoteReferenceName("origin", branchName)
	remoteRef, err := r.Reference(remoteRefName, true)
	if err != nil {
		remoteRefName = plumbing.NewRemoteReferenceName("origin", "main")
		remoteRef, err = r.Reference(remoteRefName, true)
		if err != nil {
			return emptyUpdateStatus(), nil
		}
	}
	remoteHash := remoteRef.Hash()

	if headHash == remoteHash {
		return emptyUpdateStatus(), nil
	}

	cIter, err := r.Log(&git.LogOptions{
		From: remoteHash,
	})
	if err != nil {
		return nil, err
	}
	defer cIter.Close()

	var incoming []IncomingCommit
	err = cIter.ForEach(func(c *object.Commit) error {
		if c.Hash == headHash {
			return storer.ErrStop
		}
		if len(incoming) >= 20 {
			return storer.ErrStop
		}
		msg := strings.Split(c.Message, "\n")[0]
		incoming = append(incoming, IncomingCommit{
			Oid:     c.Hash.String()[:7],
			Message: msg,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &UpdateStatus{
		UpdateAvailable: len(incoming) > 0,
		Incoming:        incoming,
	}, nil
}

func PullRepo(pluginsDir string, id string) error {
	dir, err := PluginDir(pluginsDir, id)
	if err != nil {
		return err
	}

	meta := ReadMeta(dir)
	if meta.Source != "git" {
		return fmt.Errorf("plugin %q is not a git install", id)
	}
	if meta.PinnedRef != nil {
		return nil // Pinned: do not pull
	}

	r, err := git.PlainOpen(dir)
	if err != nil {
		return err
	}

	err = r.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		Force:      true,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return err
	}

	head, err := r.Head()
	if err != nil {
		return err
	}

	branchName := head.Name().Short()
	remoteRefName := plumbing.NewRemoteReferenceName("origin", branchName)
	remoteRef, err := r.Reference(remoteRefName, true)
	if err != nil {
		remoteRefName = plumbing.NewRemoteReferenceName("origin", "main")
		remoteRef, err = r.Reference(remoteRefName, true)
		if err != nil {
			return err
		}
	}

	// Update local reference to point to remote OID
	localRefName := plumbing.NewBranchReferenceName(branchName)
	localRef := plumbing.NewHashReference(localRefName, remoteRef.Hash())
	if err := r.Storer.SetReference(localRef); err != nil {
		return err
	}

	w, err := r.Worktree()
	if err != nil {
		return err
	}

	return hardResetPreservingMeta(w, dir, remoteRef.Hash(), meta)
}

func hardResetPreservingMeta(w *git.Worktree, dir string, commit plumbing.Hash, meta RepoMeta) error {
	// go-git's hard reset removes the untracked Chronicle metadata file, so
	// restore it or the next update will treat this git install as local.
	if err := w.Reset(&git.ResetOptions{
		Commit: commit,
		Mode:   git.HardReset,
	}); err != nil {
		return err
	}
	return WriteMeta(dir, meta)
}

func PinRepo(pluginsDir string, id string, ref *string) error {
	dir, err := PluginDir(pluginsDir, id)
	if err != nil {
		return err
	}

	meta := ReadMeta(dir)
	if ref == nil {
		meta.PinnedRef = nil
		return WriteMeta(dir, meta)
	}

	headFull, err := CurrentCommitFull(dir)
	if err != nil {
		return err
	}

	isCurrentCommit := headFull != nil && (*headFull == *ref || strings.HasPrefix(*headFull, *ref))
	if isCurrentCommit {
		meta.PinnedRef = headFull
		return WriteMeta(dir, meta)
	}

	r, err := git.PlainOpen(dir)
	if err != nil {
		return err
	}

	hash, err := r.ResolveRevision(plumbing.Revision(*ref))
	if err != nil {
		return err
	}

	w, err := r.Worktree()
	if err != nil {
		return err
	}

	err = w.Checkout(&git.CheckoutOptions{
		Hash:  *hash,
		Force: true,
	})
	if err != nil {
		return err
	}

	meta.PinnedRef = ref
	return WriteMeta(dir, meta)
}

func RemovePlugin(pluginsDir string, id string) error {
	dir, err := PluginDir(pluginsDir, id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func ReadManifest(dir string) (*PluginDeps, error) {
	file := filepath.Join(dir, "chronicle-plugin.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("missing chronicle-plugin.json in the plugin repo root")
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("chronicle-plugin.json is not valid JSON")
	}

	// Unmarshal to full struct
	var pd PluginDeps
	if err := json.Unmarshal(data, &pd); err != nil {
		return nil, err
	}

	if !ValidatePluginID(pd.ID) {
		return nil, fmt.Errorf("id must be alphanumeric with . _ -")
	}

	// Plugin manifests commonly omit optional capability collections. Keep the
	// wire contract stable for every endpoint that returns a manifest-derived
	// plugin by representing omitted collections as [] instead of null.
	if pd.Provides == nil {
		pd.Provides = make([]string, 0)
	}
	if pd.Requires == nil {
		pd.Requires = make([]string, 0)
	}
	if pd.Wants == nil {
		pd.Wants = make([]string, 0)
	}
	if pd.Conflicts == nil {
		pd.Conflicts = make([]string, 0)
	}
	if pd.Replaces == nil {
		pd.Replaces = make([]string, 0)
	}

	return &pd, nil
}

func ReadBuildError(dir string) *string {
	file := filepath.Join(dir, ".chronicle-build", "error.txt")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	str := string(data)
	return &str
}

func DescribePlugin(pluginsDir string, id string) (*DiskPlugin, error) {
	dir, err := PluginDir(pluginsDir, id)
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	meta := ReadMeta(dir)
	var manifest *PluginDeps
	var buildErr *string

	manifest, err = ReadManifest(dir)
	if err != nil {
		errStr := err.Error()
		buildErr = &errStr
	} else {
		buildErr = ReadBuildError(dir)
	}

	commit, _ := CurrentCommit(dir)

	var name string
	var desc string
	var version string
	provides := make([]string, 0)
	requires := make([]string, 0)
	wants := make([]string, 0)
	conflicts := make([]string, 0)
	replaces := make([]string, 0)
	var dependencies map[string]string

	if manifest != nil {
		name = manifest.ID
		if name == "" {
			name = id
		}
		provides = manifest.Provides
		requires = manifest.Requires
		wants = manifest.Wants
		conflicts = manifest.Conflicts
		replaces = manifest.Replaces
		// To mock name/desc/version, we can extract from manifest raw
		// or read directly. Let's see: chronicle-plugin.json has name, description, version, entry, dependencies!
		// Let's decode them by parsing raw
		var raw struct {
			Name         string            `json:"name"`
			Description  string            `json:"description"`
			Version      string            `json:"version"`
			Dependencies map[string]string `json:"dependencies"`
		}
		_ = json.Unmarshal(mustReadFile(filepath.Join(dir, "chronicle-plugin.json")), &raw)
		name = raw.Name
		desc = raw.Description
		version = raw.Version
		dependencies = raw.Dependencies
	}

	if name == "" {
		name = id
	}

	return &DiskPlugin{
		ID:           id,
		Name:         name,
		Description:  desc,
		Version:      version,
		Source:       meta.Source,
		GitUrl:       meta.GitUrl,
		Commit:       commit,
		PinnedRef:    meta.PinnedRef,
		BuildError:   buildErr,
		Provides:     provides,
		Requires:     requires,
		Wants:        wants,
		Conflicts:    conflicts,
		Replaces:     replaces,
		Dependencies: dependencies,
	}, nil
}

func mustReadFile(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}

func InstalledIDs(pluginsDir string) []string {
	var ids []string
	files, err := os.ReadDir(pluginsDir)
	if err != nil {
		return ids
	}

	for _, file := range files {
		if file.IsDir() && ValidatePluginID(file.Name()) {
			ids = append(ids, file.Name())
		}
	}
	return ids
}
