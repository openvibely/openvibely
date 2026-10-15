package components

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

type jsonPreviewNode struct {
	Label, Value, Kind string
	Count              int
	Children           []jsonPreviewNode
	Branch             bool
}

// Read tokens rather than unmarshalling to maps: preserve source order and
// numeric precision. The decoder validates JSON nesting; presentation limits
// apply per branch, not to the number of tokens in a valid document.
func fileJSONPreview(content string) (jsonPreviewNode, bool) {
	d := json.NewDecoder(strings.NewReader(content))
	d.UseNumber()
	var read func(string, int) (jsonPreviewNode, bool)
	read = func(label string, depth int) (jsonPreviewNode, bool) {
		n := jsonPreviewNode{Label: label}
		token, err := d.Token()
		if err != nil {
			return n, false
		}
		delimiter, branch := token.(json.Delim)
		if !branch {
			b, err := json.Marshal(token)
			n.Value = string(b)
			n.Kind = "keyword"
			switch token.(type) {
			case string:
				n.Kind = "string"
			case json.Number:
				n.Kind = "number"
			}
			return n, err == nil
		}
		if delimiter != '{' && delimiter != '[' {
			return n, false
		}
		n.Branch = true
		n.Value = "Array"
		if delimiter == '{' {
			n.Value = "Object"
		}
		for d.More() {
			key := strconv.Itoa(n.Count)
			if delimiter == '{' {
				t, err := d.Token()
				if err != nil {
					return n, false
				}
				var ok bool
				key, ok = t.(string)
				if !ok {
					return n, false
				}
			}
			child, ok := read(key, depth+1)
			if !ok {
				return n, false
			}
			n.Count++
			if len(n.Children) < 200 {
				n.Children = append(n.Children, child)
			}
		}
		end, err := d.Token()
		return n, err == nil && ((delimiter == '{' && end == json.Delim('}')) || (delimiter == '[' && end == json.Delim(']')))
	}
	node, ok := read("", 0)
	if !ok {
		return node, false
	}
	_, err := d.Token()
	return node, err == io.EOF
}
