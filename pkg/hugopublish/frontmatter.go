package hugopublish

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseMarkdownFile splits a Hugo content file into its front matter and
// body. Only the YAML `---`-fenced form is supported (the form Chronicler
// itself always writes) — a file with no leading `---` block is treated as
// having empty front matter and the whole file as body.
func ParseMarkdownFile(raw []byte) (FrontMatter, string, error) {
	text := string(raw)
	const fence = "---"

	if !strings.HasPrefix(text, fence) {
		return FrontMatter{}, text, nil
	}

	rest := text[len(fence):]
	// Find the closing fence on its own line.
	idx := strings.Index(rest, "\n"+fence)
	if idx == -1 {
		return FrontMatter{}, text, nil
	}
	yamlBlock := rest[:idx]
	body := rest[idx+1+len(fence):]
	body = strings.TrimPrefix(body, "\n")

	var fm FrontMatter
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return FrontMatter{}, text, fmt.Errorf("parsing front matter: %w", err)
	}
	return fm, body, nil
}

// BuildFrontMatter renders fm plus body into a full Hugo content file.
func BuildFrontMatter(fm FrontMatter, body string) (string, error) {
	yamlBytes, err := yaml.Marshal(fm)
	if err != nil {
		return "", fmt.Errorf("marshalling front matter: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(yamlBytes)
	sb.WriteString("---\n\n")
	sb.WriteString(body)
	return sb.String(), nil
}
