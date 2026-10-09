package handler

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// TestEveryConsolePageSitsInsideTheContentArea pins the shell the client router depends on.
// .main-layout is a flex row, so a page that escapes .content-area turns into a sibling of
// the sidebar: the page keeps its own flex:1 and the console splits down the middle, with
// the empty .content-area occupying the left half. One stray </div> is enough to eject that
// page and every page after it, which is why this checks the parsed tree rather than a
// hand-rolled tag count - the browser's parser is the thing being pinned.
func TestEveryConsolePageSitsInsideTheContentArea(t *testing.T) {
	src := readConsoleTemplate(t)

	doc, err := html.Parse(bytes.NewBufferString(src))
	if err != nil {
		t.Fatalf("parse index.html: %v", err)
	}

	contentAreas := 0
	var contentArea *html.Node
	walkElements(doc, func(n *html.Node) {
		if n.Data == "div" && hasClass(n, "content-area") {
			contentAreas++
			if contentArea == nil {
				contentArea = n
			}
		}
	})
	if contentAreas != 1 {
		t.Fatalf("index.html must have exactly one .content-area shell, found %d", contentAreas)
	}
	if !hasClass(contentArea.Parent, "main-layout") {
		t.Error(".content-area must sit directly inside .main-layout")
	}

	pages := 0
	walkElements(doc, func(n *html.Node) {
		id := attr(n, "id")
		if n.Data != "div" || !strings.HasPrefix(id, "page-") {
			return
		}
		pages++
		if n.Parent == contentArea {
			return
		}
		t.Errorf("%s is not a child of .content-area (parent: %s) - an unbalanced </div> above line %d ejects it and every page after it into .main-layout",
			id, describeNode(contentArea.Parent), sourceLine(t, src, `id="`+id+`"`))
	})
	if pages < 25 {
		t.Fatalf("only %d #page-* divs found in index.html - the scan itself broke", pages)
	}
}

func walkElements(node *html.Node, visit func(*html.Node)) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode {
			visit(child)
			walkElements(child, visit)
		}
	}
}

func attr(node *html.Node, key string) string {
	for _, a := range node.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(node *html.Node, want string) bool {
	for _, field := range strings.Fields(attr(node, "class")) {
		if field == want {
			return true
		}
	}
	return false
}

func describeNode(node *html.Node) string {
	if node == nil {
		return "<none>"
	}
	if id := attr(node, "id"); id != "" {
		return fmt.Sprintf("#%s", id)
	}
	if classes := attr(node, "class"); classes != "" {
		return fmt.Sprintf(".%s", strings.ReplaceAll(classes, " ", "."))
	}
	return "<" + node.Data + ">"
}

func sourceLine(t *testing.T, src, needle string) int {
	t.Helper()
	at := strings.Index(src, needle)
	if at < 0 {
		return -1
	}
	return 1 + strings.Count(src[:at], "\n")
}

func readConsoleTemplate(t *testing.T) string {
	t.Helper()
	path := filepath.Join(moduleRoot(t), "web", "templates", "index.html")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
