// Package hugopublish lets a Chronicler plugin push manuscript chapters to,
// and pull them back from, a Hugo static-site git repository. It never
// persists the caller's git credentials — every function takes a token per
// call and only holds it in memory for that call's duration.
package hugopublish

// FrontMatter is the YAML front matter Chronicler writes into (and reads
// back out of) each published chapter/story file.
type FrontMatter struct {
	Title      string   `yaml:"title"`
	Date       string   `yaml:"date,omitempty"`
	Draft      bool     `yaml:"draft"`
	Author     string   `yaml:"author,omitempty"`
	Weight     *int     `yaml:"weight,omitempty"`
	Series     []string `yaml:"series,omitempty"`
	Tags       []string `yaml:"tags,omitempty"`
	Categories []string `yaml:"categories,omitempty"`
}

// StoryInfo describes one discovered story (a Hugo "leaf" of narrative
// content — either a subfolder of chapter files, or, in a flat section, the
// section itself).
type StoryInfo struct {
	Slug         string `json:"slug"`
	Title        string `json:"title"`
	Path         string `json:"path"` // repo-relative, e.g. "content/fiction/story-a"
	ChapterCount int    `json:"chapterCount"`
}

// SectionLayout classifies how a content section is organized.
type SectionLayout string

const (
	LayoutFlat    SectionLayout = "flat"    // section/chapter-1.md, section/chapter-2.md, ...
	LayoutNested  SectionLayout = "nested"  // section/story-a/{_index.md, ch1.md, ...}
	LayoutUnknown SectionLayout = "unknown" // empty section — caller must choose
)

type SectionInfo struct {
	Path    string        `json:"path"` // repo-relative, e.g. "content/fiction"
	Layout  SectionLayout `json:"layout"`
	Mixed   bool          `json:"mixed"` // true if loose files AND qualifying subfolders both exist
	Stories []StoryInfo   `json:"stories"`
}

type ProbeRequest struct {
	RepoURL string `json:"repoUrl"`
	Branch  string `json:"branch"`
	Token   string `json:"token"`
}

type ProbeResult struct {
	ConfigFile        string        `json:"configFile"`
	ConfigFormat      string        `json:"configFormat"` // "yaml" | "toml" | "json" | "none"
	ContentDir        string        `json:"contentDir"`
	ContentDirGuessed bool          `json:"contentDirGuessed"`
	Sections          []SectionInfo `json:"sections"`
}

// FileWrite is one file to write as part of a single batched publish commit.
type FileWrite struct {
	Path        string      `json:"path"` // repo-relative, e.g. "content/fiction/story-a/ch1.md"
	FrontMatter FrontMatter `json:"frontMatter"`
	Body        string      `json:"body"` // markdown, front matter excluded
}

type PublishRequest struct {
	RepoURL               string      `json:"repoUrl"`
	Branch                string      `json:"branch"`
	Token                 string      `json:"token"`
	CreateBranchIfMissing bool        `json:"createBranchIfMissing"`
	Files                 []FileWrite `json:"files"`
	CommitMessage         string      `json:"commitMessage"`
	CommitterName         string      `json:"committerName"`
	CommitterEmail        string      `json:"committerEmail"`
}

type PublishResult struct {
	CommitSHA    string   `json:"commitSha"`
	Branch       string   `json:"branch"`
	FilesWritten []string `json:"filesWritten"`
}

type ImportRequest struct {
	RepoURL   string        `json:"repoUrl"`
	Branch    string        `json:"branch"`
	Token     string        `json:"token"`
	StoryPath string        `json:"storyPath"`
	Layout    SectionLayout `json:"layout"`
}

type ImportedChapter struct {
	Filename     string      `json:"filename"`
	Title        string      `json:"title"`
	FrontMatter  FrontMatter `json:"frontMatter"`
	MarkdownBody string      `json:"markdownBody"`
}

type ImportResult struct {
	StoryTitle string            `json:"storyTitle"`
	Chapters   []ImportedChapter `json:"chapters"`
}
