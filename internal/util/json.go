package util

import (
	"encoding/json"
	"strings"
)

// JSONPayloadCandidates returns ordered, deduplicated JSON object/array candidates
// found in an LLM text reply. It handles direct JSON replies, fenced JSON/generic
// code blocks, JSON surrounded by prose, and concatenated JSON values.
func JSONPayloadCandidates(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	var candidates []string
	addCandidate := func(candidate string) {
		candidate = normalizeJSONPayloadCandidate(candidate)
		if candidate == "" {
			return
		}
		for _, existing := range candidates {
			if existing == candidate {
				return
			}
		}
		candidates = append(candidates, candidate)
	}

	if (s[0] == '{' || s[0] == '[') && json.Valid([]byte(s)) {
		addCandidate(s)
	}
	for _, block := range fencedJSONPayloads(s) {
		addCandidate(block)
	}
	for _, candidate := range balancedJSONValues(s) {
		addCandidate(candidate)
	}
	return candidates
}

// ExtractJSONObject strips markdown fences and returns the first JSON object found in the string.
func ExtractJSONObject(s string) string {
	for _, candidate := range JSONPayloadCandidates(s) {
		if strings.HasPrefix(strings.TrimSpace(candidate), "{") {
			return candidate
		}
	}
	return ""
}

// ExtractJSONArray strips markdown fences and returns the first JSON array found in the string.
func ExtractJSONArray(s string) string {
	for _, candidate := range JSONPayloadCandidates(s) {
		if strings.HasPrefix(strings.TrimSpace(candidate), "[") {
			return candidate
		}
	}
	return ""
}

// StripMarkdownFences removes ```json and ``` fences from a string.
func StripMarkdownFences(s string) string {
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```", "")
	return strings.TrimSpace(s)
}

func fencedJSONPayloads(s string) []string {
	var payloads []string
	addFencesWithPrefix := func(prefix string) {
		for searchFrom := 0; searchFrom < len(s); {
			idx := strings.Index(s[searchFrom:], prefix)
			if idx < 0 {
				return
			}
			start := searchFrom + idx + len(prefix)
			rest := s[start:]
			end := strings.Index(rest, "```")
			if end < 0 {
				return
			}
			payloads = append(payloads, strings.TrimSpace(rest[:end]))
			searchFrom = start + end + len("```")
		}
	}

	addFencesWithPrefix("```json")
	addFencesWithPrefix("```")
	return payloads
}

func normalizeJSONPayloadCandidate(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || json.Valid([]byte(candidate)) {
		return candidate
	}
	if payload := firstBalancedJSONValue(candidate); payload != "" {
		return payload
	}
	return candidate
}

func firstBalancedJSONValue(s string) string {
	values := balancedJSONValues(s)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func balancedJSONValues(s string) []string {
	values := make([]string, 0, 2)
	for offset := 0; offset < len(s); {
		start := -1
		for i := offset; i < len(s); i++ {
			if s[i] == '{' || s[i] == '[' {
				start = i
				break
			}
		}
		if start < 0 {
			break
		}

		dec := json.NewDecoder(strings.NewReader(s[start:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil || len(raw) == 0 {
			offset = start + 1
			continue
		}
		values = append(values, strings.TrimSpace(string(raw)))
		next := start + int(dec.InputOffset())
		if next <= start {
			next = start + 1
		}
		offset = next
	}
	return values
}
