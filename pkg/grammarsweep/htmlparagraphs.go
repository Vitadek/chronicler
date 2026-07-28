package grammarsweep

import (
	"strings"

	hparser "golang.org/x/net/html"
)

// extractParagraphs pulls plain text out of every top-level <p> in chapter
// HTML, mirroring what the frontend actually lints: Grammar.ts's compute()
// only walks ProseMirror `paragraph` nodes (chronicle-plugin-proofreader/
// src/lib/Grammar.ts), skipping headings/lists entirely — matching that here
// means every paragraph this extracts is one the client will genuinely ask
// the server to check, so the sweep never wastes work warming text nobody
// will request. Modeled on pkg/collab/convert.go's parse shape (same
// golang.org/x/net/html walk, same <p> boundary), simplified to plain text
// instead of a CRDT document.
func extractParagraphs(htmlStr string) []string {
	if strings.TrimSpace(htmlStr) == "" {
		return nil
	}

	root, err := hparser.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return nil
	}

	var out []string
	var walk func(n *hparser.Node)
	walk = func(n *hparser.Node) {
		if n.Type == hparser.ElementNode && strings.ToLower(n.Data) == "p" {
			// The cache is keyed on the EXACT text the client sends (see
			// proseMirrorText.ts's buildPosMap, which never trims), so the
			// text pushed to `out` must stay untrimmed — hashing a trimmed
			// version would never match a real request's cache key. Only the
			// length CHECK uses a trimmed count, mirroring Grammar.ts's
			// `text.trim().length >= minChars` filter (default minChars=12):
			// shorter paragraphs are never sent by the client, so caching
			// them would be wasted work.
			if text := textContent(n); len(strings.TrimSpace(text)) >= 12 {
				out = append(out, text)
			}
			return // paragraphs don't nest paragraphs; no need to recurse further
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

func textContent(n *hparser.Node) string {
	var b strings.Builder
	var walk func(n *hparser.Node)
	walk = func(n *hparser.Node) {
		if n.Type == hparser.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
