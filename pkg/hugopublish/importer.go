package hugopublish

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ImportStory reads a story's chapter files back out of the repo, sorted by
// front-matter weight (if every file has one) or filename otherwise. Markdown
// bodies are returned as-is — HTML conversion is the frontend plugin's job.
func ImportStory(req ImportRequest) (*ImportResult, error) {
	dir, unlock, err := EnsureClone(req.RepoURL, req.Branch, req.Token)
	if err != nil {
		return nil, err
	}
	defer unlock()

	storyAbsPath := filepath.Join(dir, filepath.FromSlash(req.StoryPath))

	storyTitle := ""
	if req.Layout == LayoutNested {
		if data, readErr := os.ReadFile(filepath.Join(storyAbsPath, "_index.md")); readErr == nil {
			if fm, _, parseErr := ParseMarkdownFile(data); parseErr == nil {
				storyTitle = fm.Title
			}
		}
	}

	entries, err := os.ReadDir(storyAbsPath)
	if err != nil {
		return nil, err
	}

	var chapters []ImportedChapter
	for _, e := range entries {
		if e.IsDir() || e.Name() == "_index.md" || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(storyAbsPath, e.Name()))
		if readErr != nil {
			return nil, readErr
		}
		fm, body, parseErr := ParseMarkdownFile(data)
		if parseErr != nil {
			return nil, parseErr
		}
		title := fm.Title
		if title == "" {
			title = strings.TrimSuffix(e.Name(), ".md")
		}
		chapters = append(chapters, ImportedChapter{
			Filename:     e.Name(),
			Title:        title,
			FrontMatter:  fm,
			MarkdownBody: body,
		})
	}

	sortChapters(chapters)

	if storyTitle == "" {
		storyTitle = filepath.Base(req.StoryPath)
	}
	return &ImportResult{StoryTitle: storyTitle, Chapters: chapters}, nil
}

// sortChapters orders by front-matter weight if every chapter has one,
// otherwise falls back to filename order.
func sortChapters(chapters []ImportedChapter) {
	allWeighted := len(chapters) > 0
	for _, c := range chapters {
		if c.FrontMatter.Weight == nil {
			allWeighted = false
			break
		}
	}
	sort.SliceStable(chapters, func(i, j int) bool {
		if allWeighted {
			return *chapters[i].FrontMatter.Weight < *chapters[j].FrontMatter.Weight
		}
		return chapters[i].Filename < chapters[j].Filename
	})
}
