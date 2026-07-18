package collab

import (
	"testing"
)

func TestHTMLToYDocAndBack(t *testing.T) {
	testCases := []struct {
		name string
		html string
	}{
		{
			name: "Simple paragraph",
			html: "<p>Hello world</p>",
		},
		{
			name: "Heading and paragraph",
			html: "<h1>Title</h1><p>Paragraph text</p>",
		},
		{
			name: "Marks",
			html: "<p>Hello <strong>bold</strong> and <em>italic</em> text.</p>",
		},
		{
			name: "Nested lists",
			html: "<ul><li><p>Item 1</p></li><li><p>Item 2</p></li></ul>",
		},
		{
			name: "Links and span marks",
			html: `<p>A <a href="https://google.com">link</a> and <span data-comment="this is a comment">commented text</span>.</p>`,
		},
		{
			name: "Epigraph",
			html: `<blockquote data-type="epigraph">This is an epigraph</blockquote>`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := HTMLToYDoc(tc.html)
			if err != nil {
				t.Fatalf("failed to convert HTML to YDoc: %v", err)
			}
			out, err := YDocToHTML(doc)
			if err != nil {
				t.Fatalf("failed to convert YDoc to HTML: %v", err)
			}
			if out != tc.html {
				t.Errorf("mismatch:\nexpected: %q\ngot:      %q", tc.html, out)
			}
		})
	}
}
