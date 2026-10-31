package openaiclient

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"io"
	"strings"
	"testing"
)

func TestNativeReplayReuseAndInvalidation(t *testing.T) {
	raw := json.RawMessage(`{ "role":"tool", "content":"unchanged <>&", "tool_call_id":"call" }`)
	message, err := DecodeCompletionsReplay(raw)
	require.NoError(t, err)
	require.Equal(t, raw, message.wireReplay())
	messages := []completionsMessage{{Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID, rawReplay: message.rawReplay}}
	prepared, err := prepareCompletionsMessages(messages)
	require.NoError(t, err)
	require.Contains(t, string(prepared.JSON()), string(raw))
	messages[0].Content = "truncated"
	prepared, err = prepareCompletionsMessages(messages)
	require.NoError(t, err)
	require.NotContains(t, string(prepared.JSON()), "unchanged")
	require.Contains(t, string(prepared.JSON()), "truncated")
	for _, row := range []string{`{"role":"user","content":null}`, `{"role":"user","content":"x","extra":true}`, `{"role":"user","role":"tool","content":"x"}`} {
		m, err := DecodeCompletionsReplay(json.RawMessage(row))
		require.NoError(t, err)
		require.Nil(t, m.wireReplay())
	}
	tool, err := DecodeCompletionsReplay(json.RawMessage(`{"role":"assistant","content":"","tool_calls":[{"id":"x","type":"function","function":{"name":"read_file","arguments":"{}"}}]}`))
	require.NoError(t, err)
	require.NotNil(t, tool.wireReplay())
	tool.ToolCalls[0].Function.Arguments = `{"path":"new"}`
	require.Nil(t, tool.wireReplay())
}

func TestTypedCompletionsBuildersAndPartialRecovery(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"content":"hello","reasoning_content":"think","tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
		`{"choices":[{"delta":{"content":" world","reasoning_content":" more","tool_calls":[{"index":0,"function":{"arguments":"\"a\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30,"reasoning_tokens":4}}`,
	}
	var stream strings.Builder
	for _, chunk := range chunks {
		stream.WriteString("data: " + chunk + "\n\n")
	}
	var capture strings.Builder
	capture.WriteString("earlier attempt ")
	c := new(Client)
	got, err := c.parseCompletionsStreamCapturing(strings.NewReader(stream.String()), nil, &capture)
	require.NoError(t, err)
	require.Equal(t, "hello world", got.text)
	require.Equal(t, "think more", got.reasoningContent)
	require.Equal(t, `{"path":"a"}`, got.toolCalls[0].Function.Arguments)
	require.Equal(t, 30, got.totalTokens)
	require.Equal(t, "earlier attempt hello world", capture.String())
	capture.Reset()
	_, err = c.parseCompletionsStreamCapturing(strings.NewReader("data: "+chunks[0]+"\n"), nil, &capture)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, "hello", capture.String())
}
