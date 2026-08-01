package collab

import (
	"bytes"
	"fmt"
	"html"
	"strings"

	"github.com/reearth/ygo/crdt"
	hparser "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTMLToYDoc parses HTML content and populates a new Yjs crdt.Doc
// with Tiptap/ProseMirror compatible XML fragments in room "default".
func HTMLToYDoc(htmlStr string) (*crdt.Doc, error) {
	doc := crdt.New()
	if htmlStr == "" {
		htmlStr = "<p></p>"
	}

	reader := strings.NewReader(htmlStr)
	rootNode, err := hparser.Parse(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Resolve the fragment outside of Transaction to avoid deadlock
	fragment := doc.GetXmlFragment("default")

	// We start the transaction to build the document.
	var buildErr error
	doc.Transact(func(txn *crdt.Transaction) {
		// Find the body or the main nodes
		nodesToProcess := findHTMLBodyOrRoot(rootNode)

		idx := 0
		for _, node := range nodesToProcess {
			err := parseBlockNode(txn, node, fragment, &idx)
			if err != nil {
				buildErr = err
				return
			}
		}
	})

	if buildErr != nil {
		return nil, buildErr
	}

	return doc, nil
}

func findHTMLBodyOrRoot(root *hparser.Node) []*hparser.Node {
	var body *hparser.Node
	var traverse func(*hparser.Node)
	traverse = func(n *hparser.Node) {
		if n.Type == hparser.ElementNode && n.DataAtom == atom.Body {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(root)

	if body != nil {
		var children []*hparser.Node
		for c := body.FirstChild; c != nil; c = nxt(c) {
			children = append(children, c)
		}
		return children
	}

	var children []*hparser.Node
	for c := root.FirstChild; c != nil; c = nxt(c) {
		children = append(children, c)
	}
	return children
}

func nxt(n *hparser.Node) *hparser.Node {
	if n == nil {
		return nil
	}
	return n.NextSibling
}

func parseBlockNode(txn *crdt.Transaction, n *hparser.Node, parentFragment *crdt.YXmlFragment, idx *int) error {
	if n.Type != hparser.ElementNode {
		// Ignore top-level whitespace or text nodes outside block elements
		return nil
	}

	nodeName := ""
	attrs := make(map[string]string)

	tag := strings.ToLower(n.Data)
	switch tag {
	case "p":
		nodeName = "paragraph"
	case "h1":
		nodeName = "heading"
		attrs["level"] = "1"
	case "h2":
		nodeName = "heading"
		attrs["level"] = "2"
	case "h3":
		nodeName = "heading"
		attrs["level"] = "3"
	case "ul":
		nodeName = "bulletList"
	case "ol":
		nodeName = "orderedList"
	case "li":
		nodeName = "listItem"
	case "blockquote":
		// Check for epigraph data-type
		isEpigraph := false
		for _, a := range n.Attr {
			if a.Key == "data-type" && a.Val == "epigraph" {
				isEpigraph = true
				break
			}
		}
		if isEpigraph {
			nodeName = "epigraph"
		} else {
			nodeName = "blockquote"
		}
	case "hr":
		nodeName = "horizontalRule"
	case "pre":
		nodeName = "codeBlock"
	default:
		// Fallback for unknown block tag
		nodeName = "paragraph"
	}

	elem := crdt.NewYXmlElement(nodeName)
	for k, v := range attrs {
		elem.SetAttribute(txn, k, v)
	}

	// For elements that are lists or other structural tags, we recurse blocks
	if nodeName == "bulletList" || nodeName == "orderedList" || nodeName == "listItem" || nodeName == "blockquote" {
		parentFragment.InsertElement(txn, *idx, elem)
		*idx++

		childIdx := 0
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == hparser.ElementNode {
				err := parseBlockNode(txn, c, &elem.YXmlFragment, &childIdx)
				if err != nil {
					return err
				}
			}
		}
		return nil
	}

	// For general text blocks: paragraph, heading, epigraph, codeBlock
	parentFragment.InsertElement(txn, *idx, elem)
	*idx++

	// Create a single YXmlText child.
	yText := crdt.NewYXmlText()
	elem.InsertText(txn, 0, yText)
	yTextLen := 0

	// If it's a codeBlock, we might have a nested <code> tag. If so, parse from the <code> tag.
	startNode := n.FirstChild
	if nodeName == "codeBlock" && startNode != nil && startNode.Type == hparser.ElementNode && strings.ToLower(startNode.Data) == "code" {
		startNode = startNode.FirstChild
	}

	activeAttrs := crdt.Attributes{}
	var parseInline func(*hparser.Node, crdt.Attributes)
	parseInline = func(curr *hparser.Node, currentAttrs crdt.Attributes) {
		if curr == nil {
			return
		}

		switch curr.Type {
		case hparser.TextNode:
			if curr.Data != "" {
				formattedAttrs := buildInsertAttributes(currentAttrs)
				yText.Insert(txn, yTextLen, curr.Data, formattedAttrs)
				yTextLen += len([]rune(curr.Data))
			}
		case hparser.ElementNode:
			// Translate formatting tags to marks
			nextAttrs := copyAttributes(currentAttrs)
			currTag := strings.ToLower(curr.Data)

			switch currTag {
			case "strong", "b":
				nextAttrs["bold"] = true
			case "em", "i":
				nextAttrs["italic"] = true
			case "u":
				nextAttrs["underline"] = true
			case "s", "del", "strike":
				nextAttrs["strike"] = true
			case "code":
				nextAttrs["code"] = true
			case "span":
				requestID := ""
				requestNote := ""
				for _, a := range curr.Attr {
					switch a.Key {
					case "data-comment":
						nextAttrs["comment"] = a.Val
					case "data-audio-token":
						nextAttrs["audio"] = a.Val
					case "data-proofread-request":
						requestID = a.Val
					case "data-proofread-note":
						requestNote = a.Val
					}
				}
				if requestID != "" {
					nextAttrs["proofreadRequest"] = map[string]any{
						"requestId": requestID,
						"note":      requestNote,
					}
				}
			case "a":
				for _, a := range curr.Attr {
					if a.Key == "href" {
						nextAttrs["link"] = map[string]any{"href": a.Val}
					}
				}
			case "br":
				// Insert hardBreak element and start a new text node inside the element
				breakElem := crdt.NewYXmlElement("hardBreak")
				elem.InsertElement(txn, elem.Len(), breakElem)
				newText := crdt.NewYXmlText()
				elem.InsertText(txn, elem.Len(), newText)
				yText = newText // Switch writing pointer
				yTextLen = 0    // Reset local length counter
				return
			}

			for c := curr.FirstChild; c != nil; c = c.NextSibling {
				parseInline(c, nextAttrs)
			}
		}
	}

	for c := startNode; c != nil; c = c.NextSibling {
		parseInline(c, activeAttrs)
	}

	return nil
}

func buildInsertAttributes(attrs crdt.Attributes) crdt.Attributes {
	res := crdt.Attributes{
		"bold":             nil,
		"italic":           nil,
		"underline":        nil,
		"strike":           nil,
		"code":             nil,
		"comment":          nil,
		"audio":            nil,
		"link":             nil,
		"proofreadRequest": nil,
	}
	for k, v := range attrs {
		res[k] = v
	}
	return res
}

func copyAttributes(orig crdt.Attributes) crdt.Attributes {
	res := crdt.Attributes{}
	for k, v := range orig {
		res[k] = v
	}
	return res
}

// YDocToHTML serializes a Yjs crdt.Doc to standard HTML.
func YDocToHTML(doc *crdt.Doc) (string, error) {
	fragment := doc.GetXmlFragment("default")
	var buf bytes.Buffer

	for _, child := range fragment.Children() {
		if elem, ok := child.(*crdt.YXmlElement); ok {
			err := renderBlockElement(&buf, elem)
			if err != nil {
				return "", err
			}
		}
	}

	return buf.String(), nil
}

func renderBlockElement(buf *bytes.Buffer, elem *crdt.YXmlElement) error {
	tag := ""
	attrs := ""

	switch elem.NodeName {
	case "paragraph":
		tag = "p"
	case "heading":
		level, _ := elem.GetAttribute("level")
		if level == "" {
			level = "1"
		}
		tag = "h" + level
	case "bulletList":
		tag = "ul"
	case "orderedList":
		tag = "ol"
	case "listItem":
		tag = "li"
	case "blockquote":
		tag = "blockquote"
	case "epigraph":
		tag = "blockquote"
		attrs = ` data-type="epigraph"`
	case "horizontalRule":
		buf.WriteString("<hr />")
		return nil
	case "codeBlock":
		buf.WriteString("<pre><code>")
		defer buf.WriteString("</code></pre>")
	default:
		tag = "p"
	}

	if tag != "" {
		buf.WriteString("<" + tag + attrs + ">")
		defer buf.WriteString("</" + tag + ">")
	}

	// Render children
	for _, child := range elem.Children() {
		switch sub := child.(type) {
		case *crdt.YXmlElement:
			if sub.NodeName == "hardBreak" {
				buf.WriteString("<br />")
			} else {
				// Nested block element (like in lists)
				err := renderBlockElement(buf, sub)
				if err != nil {
					return err
				}
			}
		case *crdt.YXmlText:
			// Render rich text delta
			delta := sub.ToDelta()
			for _, op := range delta {
				if op.Op != crdt.DeltaOpInsert {
					continue
				}
				txtVal, ok := op.Insert.(string)
				if !ok {
					continue
				}

				escaped := html.EscapeString(txtVal)
				rendered := renderFormattedText(escaped, op.Attributes)
				buf.WriteString(rendered)
			}
		}
	}

	return nil
}

func renderFormattedText(text string, attrs crdt.Attributes) string {
	res := text

	// Wrap in marks sequentially. We use a deterministic order to ensure consistent output.
	if linkVal, ok := attrs["link"]; ok {
		if linkMap, ok := linkVal.(map[string]any); ok {
			if href, ok := linkMap["href"].(string); ok {
				res = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(href), res)
			}
		} else if linkMap, ok := linkVal.(crdt.Attributes); ok {
			if href, ok := linkMap["href"].(string); ok {
				res = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(href), res)
			}
		}
	}
	if commentVal, ok := attrs["comment"].(string); ok {
		res = fmt.Sprintf(`<span data-comment="%s">%s</span>`, html.EscapeString(commentVal), res)
	}
	if audioVal, ok := attrs["audio"].(string); ok {
		res = fmt.Sprintf(`<span data-audio-token="%s">%s</span>`, html.EscapeString(audioVal), res)
	}
	if requestVal, ok := attrs["proofreadRequest"]; ok {
		requestID := ""
		requestNote := ""
		if requestMap, ok := requestVal.(map[string]any); ok {
			requestID, _ = requestMap["requestId"].(string)
			requestNote, _ = requestMap["note"].(string)
		} else if requestMap, ok := requestVal.(crdt.Attributes); ok {
			requestID, _ = requestMap["requestId"].(string)
			requestNote, _ = requestMap["note"].(string)
		}

		if requestID != "" {
			requestAttrs := fmt.Sprintf(` data-proofread-request="%s"`, html.EscapeString(requestID))
			if requestNote != "" {
				requestAttrs += fmt.Sprintf(` data-proofread-note="%s"`, html.EscapeString(requestNote))
			}
			res = "<span" + requestAttrs + ">" + res + "</span>"
		}
	}
	if attrs["code"] == true {
		res = "<code>" + res + "</code>"
	}
	if attrs["strike"] == true {
		res = "<s>" + res + "</s>"
	}
	if attrs["underline"] == true {
		res = "<u>" + res + "</u>"
	}
	if attrs["italic"] == true {
		res = "<em>" + res + "</em>"
	}
	if attrs["bold"] == true {
		res = "<strong>" + res + "</strong>"
	}

	return res
}
