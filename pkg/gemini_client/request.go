package geminiclient

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/openvibely/openvibely/internal/llm/requestjson"
)

// requestPreparation is scoped to one Send. Permission filters are reevaluated
// every turn; only unchanged message and schema encodings are reused.
type requestPreparation struct {
	contents     []Content
	items        []any
	declarations []functionDeclaration
	tools        *requestjson.Field
}

func (c *Client) prepareRequest(contents []Content, defs []ToolDefinition, opts *Options, cache *requestPreparation) ([]byte, int, error) {
	wire := wireContents(contents)
	items := make([]any, len(wire))
	snapshots := make([]Content, len(wire))
	for i, content := range wire {
		if i < len(cache.contents) && reflect.DeepEqual(content, cache.contents[i]) {
			items[i] = cache.items[i]
			snapshots[i] = cache.contents[i]
		} else {
			raw, err := requestjson.MarshalValue(contentValue(content))
			if err != nil {
				return nil, 0, err
			}
			items[i] = json.RawMessage(raw)
			snapshots[i] = CloneContents([]Content{content})[0]
		}
	}
	cache.contents, cache.items = snapshots, items
	field, err := requestjson.PrepareItems("contents", items)
	if err != nil {
		return nil, 0, err
	}
	metadata := map[string]any{}
	media := wire
	if strings.TrimSpace(opts.System) != "" {
		system := Content{Parts: []Part{{Text: opts.System}}}
		metadata["systemInstruction"] = contentValue(system)
		media = append(append([]Content(nil), wire...), system)
	}
	var decls []functionDeclaration
	for _, def := range defs {
		if opts.ToolFilter != nil && !opts.ToolFilter(def.Name) {
			continue
		}
		decls = append(decls, functionDeclaration{Name: def.Name, Description: def.Description, ParametersJSONSchema: def.parameterSchema(), ResponseJSONSchema: def.ResponseJSONSchema})
	}
	fields := map[string]*requestjson.Field{}
	if len(decls) > 0 {
		if cache.tools == nil || !reflect.DeepEqual(decls, cache.declarations) {
			cache.tools, err = requestjson.Prepare("tools", []map[string]any{{"functionDeclarations": decls}})
			if err != nil {
				return nil, 0, err
			}
			// Own mutable schema bytes so later schema changes invalidate the cache.
			cache.declarations = append([]functionDeclaration(nil), decls...)
			for i := range cache.declarations {
				cache.declarations[i].ParametersJSONSchema = append(json.RawMessage(nil), decls[i].ParametersJSONSchema...)
				cache.declarations[i].ResponseJSONSchema = append(json.RawMessage(nil), decls[i].ResponseJSONSchema...)
			}
		}
		fields["tools"] = cache.tools
		metadata["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "AUTO"}}
	}
	gen := map[string]any{"maxOutputTokens": opts.MaxOutputTokens}
	if opts.Temperature != nil {
		gen["temperature"] = *opts.Temperature
	}
	if opts.ReasoningEffort != "" {
		level, err := thinkingLevel(opts.Model, opts.ReasoningEffort)
		if err != nil {
			return nil, 0, err
		}
		gen["thinkingConfig"] = map[string]any{"thinkingLevel": level}
	}
	metadata["generationConfig"] = gen
	prepared := requestjson.Metadata{Value: metadata, Fields: fields}
	size, err := field.Size(prepared)
	if err != nil {
		return nil, 0, err
	}
	var envelope map[string]any
	if c.isOAuth() {
		envelope = map[string]any{"model": opts.Model, "project": c.project}
		if !c.legacyOAuth {
			envelope["requestId"] = newRequestID()
			envelope["userAgent"] = "\x61\x6e\x74\x69\x67\x72\x61\x76\x69\x74\x79"
			envelope["requestType"] = "agent"
		}
		n, err := requestjson.Size(envelope)
		if err != nil {
			return nil, 0, err
		}
		size += n + requestjson.StringSize("request") + 2
	}
	tokens := estimateContentTokens(size, media)
	body, err := field.Marshal(prepared)
	if err != nil {
		return nil, 0, err
	}
	if envelope != nil {
		envelope["request"] = json.RawMessage(body)
		body, err = requestjson.MarshalValue(envelope)
	}
	return body, tokens, err
}

// Project provider fields into the shared JSON helpers' supported shapes. Large
// strings and borrowed values can then be counted without serializing them.
func contentValue(c Content) map[string]any {
	out := map[string]any{}
	if c.Role != "" {
		out["role"] = c.Role
	}
	var parts []any
	if c.Parts != nil {
		parts = make([]any, len(c.Parts))
	}
	for i, p := range c.Parts {
		parts[i] = partValue(p)
	}
	out["parts"] = parts
	return out
}
func inlineValue(v *InlineData) any { return map[string]any{"mimeType": v.MimeType, "data": v.Data} }
func fileValue(v *FileData) any {
	out := map[string]any{"fileUri": v.FileURI}
	if v.MimeType != "" {
		out["mimeType"] = v.MimeType
	}
	return out
}
func partValue(p Part) map[string]any {
	out := map[string]any{}
	if p.Text != "" {
		out["text"] = p.Text
	}
	if p.Thought {
		out["thought"] = true
	}
	if p.ThoughtSignature != "" {
		out["thoughtSignature"] = p.ThoughtSignature
	}
	if p.InlineData != nil {
		out["inlineData"] = inlineValue(p.InlineData)
	}
	if p.FileData != nil {
		out["fileData"] = fileValue(p.FileData)
	}
	if len(p.VideoMetadata) > 0 {
		out["videoMetadata"] = p.VideoMetadata
	}
	if len(p.ExecutableCode) > 0 {
		out["executableCode"] = p.ExecutableCode
	}
	if len(p.CodeExecutionResult) > 0 {
		out["codeExecutionResult"] = p.CodeExecutionResult
	}
	if p.FunctionCall != nil {
		f := p.FunctionCall
		v := map[string]any{"name": f.Name}
		if f.ID != "" {
			v["id"] = f.ID
		}
		if len(f.Args) > 0 {
			v["args"] = f.Args
		}
		if f.WillContinue {
			v["willContinue"] = true
		}
		if len(f.PartialArgs) > 0 {
			v["partialArgs"] = f.PartialArgs
		}
		out["functionCall"] = v
	}
	if p.FunctionResponse != nil {
		f := p.FunctionResponse
		v := map[string]any{"name": f.Name, "response": f.Response}
		if f.ID != "" {
			v["id"] = f.ID
		}
		if f.Scheduling != "" {
			v["scheduling"] = f.Scheduling
		}
		if len(f.Parts) > 0 {
			parts := make([]any, len(f.Parts))
			for i, p := range f.Parts {
				m := map[string]any{}
				if p.InlineData != nil {
					m["inlineData"] = inlineValue(p.InlineData)
				}
				if p.FileData != nil {
					m["fileData"] = fileValue(p.FileData)
				}
				parts[i] = m
			}
			v["parts"] = parts
		}
		out["functionResponse"] = v
	}
	return out
}
