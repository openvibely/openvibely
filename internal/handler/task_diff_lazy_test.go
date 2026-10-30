package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"golang.org/x/net/html"
)

// Exercise the same per-file requests as the browser so existing source-selection
// tests still check the actual diff text, rather than only the new file headers.
func loadInlineTaskDiffCards(t *testing.T, h *Handler, e *echo.Echo, taskID, markup string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	var cards []*html.Node
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		for _, attr := range node.Attr {
			if attr.Key == "data-diff-index" || (attr.Key == "data-diff-lazy-url" && strings.Contains(attr.Val, "view=inline")) {
				cards = append(cards, node)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	for _, card := range cards {
		var endpoint, path, index, revision string
		for _, attr := range card.Attr {
			if attr.Key == "data-diff-lazy-url" {
				endpoint = attr.Val
			}
			if attr.Key == "data-diff-file-path" || attr.Key == "data-diff-path" {
				path = attr.Val
			}
			if attr.Key == "data-diff-index" {
				index = attr.Val
			}
			if attr.Key == "data-file-revision" {
				revision = attr.Val
			}
		}
		if endpoint == "" {
			endpoint = "/tasks/" + taskID + "/changes/file?file_index=" + index + "&view=inline&review=true&chunk=0&revision=" + url.QueryEscape(revision)
		}
		request := httptest.NewRequest(http.MethodGet, endpoint+"&file_path="+url.QueryEscape(path), nil)
		response := httptest.NewRecorder()
		c := e.NewContext(request, response)
		c.SetParamNames("taskId")
		c.SetParamValues(taskID)
		if err := h.GetTaskChangesFile(c); err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
		if response.Code != http.StatusOK {
			t.Fatalf("load %s: status %d", path, response.Code)
		}
		nodes, err := html.ParseFragment(strings.NewReader(response.Body.String()), card.Parent)
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range nodes {
			card.Parent.InsertBefore(node, card)
		}
		card.Parent.RemoveChild(card)
	}
	var result bytes.Buffer
	if err := html.Render(&result, doc); err != nil {
		t.Fatal(err)
	}
	return result.String()
}
