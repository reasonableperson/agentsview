package parser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const haxTestID = "12345678-1234-4234-8234-123456789abc"
const haxTestHeader = `{"type":"session","version":1,"hax_version":"0.6.0","id":"` + haxTestID + `","timestamp":"2026-10-01T12:00:00Z","cwd":"/workspace/example","git_branch":"main","provider":"openai","model":"gpt-5.4","effort":"high"}`

func writeHaxTestSource(t *testing.T, lines ...string) (Provider, SourceRef, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "example.1234", "2026-10-01T12-00-00Z_"+haxTestID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	provider, ok := NewProvider(AgentHax, ProviderConfig{Roots: []string{root}, Machine: "local"})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	return provider, sources[0], path
}

func parseHaxTestSource(t *testing.T, lines ...string) ParseResult {
	t.Helper()
	provider, source, _ := writeHaxTestSource(t, lines...)
	outcome, err := provider.Parse(t.Context(), ParseRequest{Source: source})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	assert.True(t, outcome.ForceReplace)
	return outcome.Results[0].Result
}

func TestHaxMessagesToolsAndUsage(t *testing.T) {
	result := parseHaxTestSource(t, haxTestHeader,
		`{"kind":"turn_boundary"}`,
		`{"kind":"user","text":"Inspect the project"}`,
		`{"kind":"reasoning","reasoning_text":"Read the file first"}`,
		`{"kind":"tool_call","call_id":"call-1","tool_name":"read","arguments":"{\"path\":\"README.md\"}"}`,
		`{"kind":"tool_result","call_id":"call-1","output":"file contents\n"}`,
		`{"kind":"assistant","text":"Done"}`,
		`{"kind":"turn_usage","provider":"openai","model":"gpt-5.4","usage":{"input":100,"in_tokens":65,"cached":30,"cache_write":5,"output":12,"cost_total":0.25,"cost_estimated":true}}`,
		`{"type":"user_turn","elapsed_ms":1000}`,
	)
	assert.Equal(t, "hax:"+haxTestID, result.Session.ID)
	assert.Equal(t, AgentHax, result.Session.Agent)
	assert.Equal(t, "local", result.Session.Machine)
	assert.Equal(t, "/workspace/example", result.Session.Cwd)
	assert.Equal(t, "main", result.Session.GitBranch)
	assert.Equal(t, "Inspect the project", result.Session.FirstMessage)
	assert.Equal(t, 1, result.Session.UserMessageCount)
	assert.Equal(t, 5, result.Session.MessageCount)
	assert.Equal(t, 12, result.Session.TotalOutputTokens)
	assert.Equal(t, 100, result.Session.PeakContextTokens)
	require.Len(t, result.Messages, 5)
	assert.True(t, result.Messages[1].HasThinking)
	assert.Equal(t, "Read the file first", result.Messages[1].ThinkingText)
	require.Len(t, result.Messages[2].ToolCalls, 1)
	assert.Equal(t, "call-1", result.Messages[2].ToolCalls[0].ToolUseID)
	assert.Equal(t, "Read", result.Messages[2].ToolCalls[0].Category)
	assert.JSONEq(t, `{"path":"README.md"}`, result.Messages[2].ToolCalls[0].InputJSON)
	require.Len(t, result.Messages[3].ToolResults, 1)
	assert.Equal(t, `"file contents\n"`, result.Messages[3].ToolResults[0].ContentRaw)
	assert.Equal(t, "gpt-5.4", result.Messages[4].Model)
	assert.Equal(t, "high", result.Messages[4].ReasoningEffort)
	assert.True(t, result.Messages[4].Timestamp.IsZero(), "hax does not record message timestamps")
	require.Len(t, result.UsageEvents, 1)
	event := result.UsageEvents[0]
	assert.Equal(t, 65, event.InputTokens)
	assert.Equal(t, 30, event.CacheReadInputTokens)
	assert.Equal(t, 5, event.CacheCreationInputTokens)
	assert.Equal(t, "openai", event.ProviderID)
	assert.Equal(t, "estimated", event.CostStatus)
	require.NotNil(t, event.Cost)
	assert.Equal(t, int64(250000), event.Cost.Microdollars)
	assert.Equal(t, "2026-10-01T12:00:00Z", event.OccurredAt)
}

func TestHaxUndoRetainsBillingAndSelection(t *testing.T) {
	result := parseHaxTestSource(t, haxTestHeader,
		`{"kind":"turn_boundary"}`,
		`{"kind":"user","text":"First"}`,
		`{"kind":"assistant","text":"First answer"}`,
		`{"kind":"turn_usage","usage":{"input":10,"output":2}}`,
		`{"kind":"user","origin":"compact_seed","text":"Summary"}`,
		`{"kind":"turn_boundary"}`,
		`{"kind":"user","text":"Undone"}`,
		`{"kind":"assistant","text":"Undone answer"}`,
		`{"kind":"turn_usage","usage":{"input":20,"output":3}}`,
		`{"type":"selection","provider":"anthropic","model":"claude-sonnet-4-6"}`,
		`{"type":"undo","keep_user_turns":1}`,
		`{"kind":"turn_boundary"}`,
		`{"kind":"user","origin":"future-origin","text":"Replacement"}`,
		`{"kind":"assistant","text":"New answer"}`,
		`{"kind":"turn_usage","usage":{"input":30,"output":4}}`,
	)
	assert.Equal(t, 2, result.Session.UserMessageCount)
	assert.Equal(t, 9, result.Session.TotalOutputTokens)
	require.Len(t, result.UsageEvents, 3)
	require.Len(t, result.Messages, 5)
	assert.Equal(t, "Summary", result.Messages[2].Content)
	assert.True(t, result.Messages[2].IsCompactBoundary)
	assert.Equal(t, "Replacement", result.Messages[3].Content)
	assert.Equal(t, "claude-sonnet-4-6", result.Messages[4].Model)
	assert.Equal(t, "anthropic", result.Messages[4].ProviderID)
	assert.Empty(t, result.Messages[4].ReasoningEffort)
	assert.Equal(t, "claude-sonnet-4-6", result.UsageEvents[2].Model)
	assert.Equal(t, "line-15", result.Messages[4].SourceUUID)
}

func TestHaxForkExcludesInheritedUsage(t *testing.T) {
	header := strings.TrimSuffix(haxTestHeader, "}") + `,"forked_from":"parent"}`
	result := parseHaxTestSource(t, header,
		`{"kind":"user","text":"Inherited question","inherited":true}`,
		`{"kind":"assistant","text":"Inherited answer","inherited":true}`,
		`{"kind":"turn_usage","usage":{"input":100,"output":10,"cost_total":1},"inherited":true}`,
		`{"kind":"user","text":"New question"}`,
		`{"kind":"turn_usage","usage":{"input":40,"cached":10,"cache_write":5,"output":2,"cost_total":0}}`,
	)
	assert.Equal(t, "hax:parent", result.Session.ParentSessionID)
	assert.Equal(t, RelFork, result.Session.RelationshipType)
	require.Len(t, result.Messages, 3)
	require.Len(t, result.UsageEvents, 1)
	assert.Equal(t, 25, result.UsageEvents[0].InputTokens)
	require.NotNil(t, result.UsageEvents[0].Cost)
	assert.Zero(t, result.UsageEvents[0].Cost.Microdollars)
	assert.Equal(t, 2, result.Session.TotalOutputTokens)
}

func TestHaxUndoAndMalformedRecords(t *testing.T) {
	for _, tc := range []struct {
		name, undo string
		want       int
	}{
		{name: "undo all", undo: `{"type":"undo","keep_user_turns":0}`, want: 0},
		{name: "negative ignored", undo: `{"type":"undo","keep_user_turns":-1}`, want: 2},
		{name: "missing ignored", undo: `{"type":"undo"}`, want: 2},
		{name: "string ignored", undo: `{"type":"undo","keep_user_turns":"0"}`, want: 2},
		{name: "fraction ignored", undo: `{"type":"undo","keep_user_turns":0.5}`, want: 2},
		{name: "excess ignored", undo: `{"type":"undo","keep_user_turns":3}`, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := parseHaxTestSource(t, haxTestHeader,
				`{"kind":"turn_boundary"}`, `{"kind":"user","text":"Question"}`,
				`{broken`, `{"kind":"future"}`, `{"kind":"assistant","text":"Answer"}`, tc.undo,
			)
			assert.Len(t, result.Messages, tc.want)
			assert.Equal(t, 1, result.Session.MalformedLines)
		})
	}
}

func TestHaxHeaderValidationAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, header      string
		wantErr, wantSkip bool
	}{
		{name: "invalid JSON", header: `{broken`, wantErr: true},
		{name: "other format", header: `{"type":"session","id":"pi"}`, wantSkip: true},
		{name: "future version", header: strings.Replace(haxTestHeader, `"version":1`, `"version":2`, 1), wantErr: true},
		{name: "missing ID", header: `{"type":"session","version":1,"hax_version":"0.6.0"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, source, _ := writeHaxTestSource(t, tc.header)
			outcome, err := provider.Parse(t.Context(), ParseRequest{Source: source})
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Empty(t, outcome.Results)
			}
		})
	}
	_, _, path := writeHaxTestSource(t, haxTestHeader)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := parseHaxSession(ctx, path, "local")
	require.ErrorIs(t, err, context.Canceled)
}

func TestHaxTornFinalLineAndOversizedRecord(t *testing.T) {
	_, _, path := writeHaxTestSource(t, haxTestHeader)
	require.NoError(t, os.WriteFile(path, []byte(haxTestHeader+"\n"+`{"kind":"user","text":"Question"}`+"\n"+`{"kind":"assistant"`), 0o600))
	session, messages, err := parseHaxSession(t.Context(), path, "local")
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, 1, session.MalformedLines)
	require.NoError(t, os.WriteFile(path, []byte(haxTestHeader+"\n"+strings.Repeat("x", maxLineSize+1)+"\n"), 0o600))
	_, _, err = parseHaxSession(t.Context(), path, "local")
	require.ErrorContains(t, err, "exceeds")
}

func TestHaxSourceLookupWatchAndReplay(t *testing.T) {
	provider, source, path := writeHaxTestSource(t, haxTestHeader, `{"kind":"user","text":"Question"}`)
	root := filepath.Dir(filepath.Dir(path))
	for _, unrelated := range []string{"auth.json", "state.json", "history", "root.jsonl", "example.1234/nested/session.jsonl"} {
		file := filepath.Join(root, unrelated)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(haxTestHeader+"\n"), 0o600))
	}
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	found, ok, err := provider.FindSource(t.Context(), FindSourceRequest{RawSessionID: "hax:" + haxTestID})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, source.Key, found.Key)
	plan, err := provider.WatchPlan(t.Context())
	require.NoError(t, err)
	assert.NotEmpty(t, plan)
	first, err := provider.Parse(t.Context(), ParseRequest{Source: source})
	require.NoError(t, err)
	second, err := provider.Parse(t.Context(), ParseRequest{Source: source})
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), first.Results[0].Result.Session.StartedAt)
	require.NoError(t, os.Remove(path))
	changed, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{Path: path, WatchRoot: root})
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, source.Key, changed[0].Key)
}
