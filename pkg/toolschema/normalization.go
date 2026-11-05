package toolschema

import "unsafe"

// Only the keywords used by normalization are retained. Descriptions, enums,
// defaults and other validation data remain the compiled validator's concern.
// All nodes are immutable after publication through the validator cache.
type normalizationNode struct {
	properties []normalizationProperty
	items      *normalizationNode
}

type normalizationProperty struct {
	name     string
	omitNull bool
	child    *normalizationNode
}

func prepareNormalization(s map[string]any) (*normalizationNode, int) {
	node := &normalizationNode{}
	size := int(unsafe.Sizeof(*node))
	props, _ := s["properties"].(map[string]any)
	required := requiredFields(s)
	node.properties = make([]normalizationProperty, 0, len(props))
	// Include the complete backing array, even for unsupported property values.
	size += cap(node.properties) * int(unsafe.Sizeof(normalizationProperty{}))
	for name, raw := range props {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		prepared, childSize := prepareNormalization(child)
		kind, _ := child["type"].(string)
		node.properties = append(node.properties, normalizationProperty{name: name, omitNull: !required[name] && kind != "" && kind != "null", child: prepared})
		size += len(name) + childSize
	}
	if child, ok := s["items"].(map[string]any); ok {
		var childSize int
		node.items, childSize = prepareNormalization(child)
		size += childSize
	}
	return node, size
}

func (node *normalizationNode) normalize(value any) {
	switch v := value.(type) {
	case map[string]any:
		for _, prop := range node.properties {
			val, present := v[prop.name]
			if !present {
				continue
			}
			if val == nil && prop.omitNull {
				delete(v, prop.name)
				continue
			}
			prop.child.normalize(val)
		}
	case []any:
		if node.items != nil {
			for _, item := range v {
				node.items.normalize(item)
			}
		}
	}
}
