package hugopublish

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var configCandidates = []string{
	"hugo.yaml", "hugo.yml", "config.yaml", "config.yml",
	"hugo.toml", "config.toml",
	"hugo.json", "config.json",
	"config/_default/hugo.yaml", "config/_default/hugo.yml",
	"config/_default/hugo.toml", "config/_default/hugo.json",
}

func formatOf(filename string) string {
	switch {
	case strings.HasSuffix(filename, ".yaml"), strings.HasSuffix(filename, ".yml"):
		return "yaml"
	case strings.HasSuffix(filename, ".toml"):
		return "toml"
	case strings.HasSuffix(filename, ".json"):
		return "json"
	default:
		return "none"
	}
}

// Probe clones (or reuses a cached clone of) repoURL, locates the Hugo
// config, and classifies each top-level content section as flat
// (chapters directly in the folder), nested (subfolders, each one a
// story), or unknown (empty). It never writes to the repo.
func Probe(req ProbeRequest) (*ProbeResult, error) {
	dir, unlock, err := EnsureClone(req.RepoURL, req.Branch, req.Token)
	if err != nil {
		return nil, err
	}
	defer unlock()

	result := &ProbeResult{ConfigFormat: "none", ContentDir: "content", ContentDirGuessed: true}

	for _, candidate := range configCandidates {
		full := filepath.Join(dir, candidate)
		data, readErr := os.ReadFile(full)
		if readErr != nil {
			continue
		}
		result.ConfigFile = candidate
		result.ConfigFormat = formatOf(candidate)
		if result.ConfigFormat == "yaml" {
			var cfg map[string]interface{}
			if yaml.Unmarshal(data, &cfg) == nil {
				if cd, ok := cfg["contentDir"].(string); ok && cd != "" {
					result.ContentDir = cd
					result.ContentDirGuessed = false
				} else {
					result.ContentDirGuessed = false // config found, contentDir absent means the "content" default is authoritative, not a guess
				}
			}
		}
		break
	}

	contentPath := filepath.Join(dir, result.ContentDir)
	sectionEntries, readErr := os.ReadDir(contentPath)
	if readErr != nil {
		// No content dir yet — legitimate for a brand-new site. Report zero
		// sections rather than erroring.
		result.Sections = []SectionInfo{}
		return result, nil
	}

	sections := make([]SectionInfo, 0, len(sectionEntries))
	for _, entry := range sectionEntries {
		if !entry.IsDir() {
			continue
		}
		sections = append(sections, classifySection(contentPath, result.ContentDir, entry.Name()))
	}
	result.Sections = sections
	return result, nil
}

// classifySection applies the flat-vs-nested heuristic to one top-level
// content section. sectionRelPath is repo-relative (e.g. "content/fiction").
func classifySection(contentAbsPath, contentDir, sectionName string) SectionInfo {
	sectionAbsPath := filepath.Join(contentAbsPath, sectionName)
	sectionRelPath := filepath.ToSlash(filepath.Join(contentDir, sectionName))

	entries, err := os.ReadDir(sectionAbsPath)
	if err != nil {
		return SectionInfo{Path: sectionRelPath, Layout: LayoutUnknown, Stories: []StoryInfo{}}
	}

	var looseMd []string
	var subdirs []string
	for _, e := range entries {
		if e.IsDir() {
			subdirs = append(subdirs, e.Name())
		} else if strings.HasSuffix(e.Name(), ".md") && e.Name() != "_index.md" {
			looseMd = append(looseMd, e.Name())
		}
	}

	var stories []StoryInfo
	for _, sub := range subdirs {
		subAbs := filepath.Join(sectionAbsPath, sub)
		subEntries, err := os.ReadDir(subAbs)
		if err != nil {
			continue
		}
		hasIndex := false
		mdCount := 0
		for _, se := range subEntries {
			if se.IsDir() {
				continue
			}
			if se.Name() == "_index.md" {
				hasIndex = true
			} else if strings.HasSuffix(se.Name(), ".md") {
				mdCount++
			}
		}
		if hasIndex || mdCount >= 2 {
			stories = append(stories, StoryInfo{
				Slug:         sub,
				Title:        storyTitle(subAbs, sub, hasIndex),
				Path:         filepath.ToSlash(filepath.Join(sectionRelPath, sub)),
				ChapterCount: mdCount,
			})
		}
	}

	if len(stories) > 0 {
		return SectionInfo{
			Path:    sectionRelPath,
			Layout:  LayoutNested,
			Mixed:   len(looseMd) > 0,
			Stories: stories,
		}
	}
	if len(looseMd) > 0 {
		return SectionInfo{
			Path:   sectionRelPath,
			Layout: LayoutFlat,
			Stories: []StoryInfo{{
				Slug:         sectionName,
				Title:        sectionName,
				Path:         sectionRelPath,
				ChapterCount: len(looseMd),
			}},
		}
	}
	return SectionInfo{Path: sectionRelPath, Layout: LayoutUnknown, Stories: []StoryInfo{}}
}

func storyTitle(storyAbsPath, slug string, hasIndex bool) string {
	if !hasIndex {
		return slug
	}
	data, err := os.ReadFile(filepath.Join(storyAbsPath, "_index.md"))
	if err != nil {
		return slug
	}
	fm, _, err := ParseMarkdownFile(data)
	if err != nil || fm.Title == "" {
		return slug
	}
	return fm.Title
}
