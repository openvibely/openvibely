package geminiclient

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
)

// estimateRequestTokens counts image inputs separately from serialized text.
// Only protocol media fields are discounted; tool arguments and results remain text.
func estimateRequestTokens(body []byte) int {
	var envelope struct {
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return tokenestimate.FromByteCount(len(body))
	}
	payload := body
	if len(envelope.Request) != 0 {
		payload = envelope.Request
	}
	var req struct {
		Contents          []Content `json:"contents"`
		SystemInstruction *Content  `json:"systemInstruction"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return tokenestimate.FromByteCount(len(body))
	}
	contents := req.Contents
	if req.SystemInstruction != nil {
		contents = append(contents, *req.SystemInstruction)
	}
	return estimateContentTokens(len(body), contents)
}

func ensureRequestFits(body []byte, opts *Options) error {
	return ensureTokensFit(estimateRequestTokens(body), opts)
}
func (c *Client) buildRequestBody(contents []Content, defs []ToolDefinition, opts *Options) ([]byte, error) {
	body, _, err := c.prepareRequest(contents, defs, opts, &requestPreparation{})
	return body, err
}
