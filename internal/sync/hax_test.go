package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/parser"
)

func TestHaxSyncAppendsUndoAndUsage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "example.1234", "2026-10-01T12-00-00Z_12345678-1234-4234-8234-123456789abc.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	initial := strings.Join([]string{
		`{"type":"session","version":1,"hax_version":"0.6.0","id":"12345678-1234-4234-8234-123456789abc","timestamp":"2026-10-01T12:00:00Z","cwd":"/workspace/example","provider":"openai","model":"gpt-5.4"}`,
		`{"kind":"turn_boundary"}`,
		`{"kind":"user","text":"Question"}`,
		`{"kind":"tool_call","call_id":"call-1","tool_name":"read","arguments":"{\"path\":\"README.md\"}"}`,
		`{"kind":"tool_result","call_id":"call-1","output":"File contents"}`,
		`{"kind":"assistant","text":"Answer"}`,
		`{"kind":"turn_usage","usage":{"input":10,"output":2,"cost_total":0.01}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(initial), 0o600))
	database := openTestDB(t)
	engine := NewEngine(t.Context(), database, EngineConfig{
		AgentDirs: map[parser.AgentType][]string{parser.AgentHax: {root}},
		Machine:   "local", DisableSignalRecomputation: true,
	})
	t.Cleanup(engine.Close)
	sessionID := "hax:12345678-1234-4234-8234-123456789abc"
	require.Equal(t, 1, engine.SyncAll(t.Context(), nil).Synced)
	messages, err := database.GetAllMessages(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, messages, 3)
	require.Len(t, messages[1].ToolCalls, 1)
	assert.Equal(t, "File contents", messages[1].ToolCalls[0].ResultContent)
	originalMessageID := messages[0].ID
	usage, err := database.GetUsageEvents(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, usage, 1)
	assert.Equal(t, 2, usage[0].OutputTokens)
	assert.Zero(t, engine.SyncAll(t.Context(), nil).Synced)

	appended := initial + strings.Join([]string{
		`{"kind":"turn_boundary"}`,
		`{"kind":"user","text":"Second question"}`,
		`{"kind":"assistant","text":"Second answer"}`,
		`{"kind":"turn_usage","usage":{"input":20,"output":3}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(appended), 0o600))
	require.Equal(t, 1, engine.SyncAll(t.Context(), nil).Synced)
	messages, err = database.GetAllMessages(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, messages, 5)
	assert.Equal(t, originalMessageID, messages[0].ID)

	require.NoError(t, os.WriteFile(path, []byte(appended+`{"type":"undo","keep_user_turns":1}`+"\n"), 0o600))
	require.Equal(t, 1, engine.SyncAll(t.Context(), nil).Synced)
	messages, err = database.GetAllMessages(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, messages, 3)
	assert.Equal(t, originalMessageID, messages[0].ID)
	usage, err = database.GetUsageEvents(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, usage, 2)
	session, err := database.GetSessionFull(t.Context(), sessionID)
	require.NoError(t, err)
	require.NotNil(t, session)
	assert.Equal(t, "hax", session.Agent)
	assert.Equal(t, 1, session.UserMessageCount)
	assert.Equal(t, 5, session.TotalOutputTokens)
	assert.Equal(t, 20, session.PeakContextTokens)

	require.NoError(t, os.WriteFile(path, []byte(appended+`{"type":"undo","keep_user_turns":0}`+"\n"), 0o600))
	require.Equal(t, 1, engine.SyncAllForceParse(t.Context(), nil).Synced)
	messages, err = database.GetAllMessages(t.Context(), sessionID)
	require.NoError(t, err)
	assert.Empty(t, messages)
	usage, err = database.GetUsageEvents(t.Context(), sessionID)
	require.NoError(t, err)
	assert.Len(t, usage, 2)
}
