package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/parser"
)

func TestHaxDirectoryOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, state      string
		config, override bool
	}{
		{name: "home default"},
		{name: "XDG state default", state: "state"},
		{name: "relative XDG state", state: "relative"},
		{name: "config overrides XDG", state: "state", config: true},
		{name: "environment overrides config", state: "state", config: true, override: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupTestEnv(t)
			home := canonicalTempDir(t)
			setTestHome(t, home)
			t.Chdir(home)
			t.Setenv("XDG_STATE_HOME", "")
			t.Setenv("HAX_SESSIONS_DIR", "")
			want := filepath.Join(home, ".local", "state", "hax", "sessions")
			if tc.state != "" {
				state := filepath.Join(home, "state")
				if tc.state == "relative" {
					state = "state"
				}
				t.Setenv("XDG_STATE_HOME", state)
				want = filepath.Join(home, "state", "hax", "sessions")
			}
			settings := map[string]any{}
			if tc.config {
				want = filepath.Join(home, "configured")
				settings["agents"] = map[string]any{"hax": map[string]any{"dirs": []string{want}}}
			}
			if tc.override {
				want = filepath.Join(home, "override")
				t.Setenv("HAX_SESSIONS_DIR", want)
			}
			writeConfig(t, dir, settings)
			cfg, err := LoadMinimal()
			require.NoError(t, err)
			assert.Equal(t, []string{want}, cfg.ResolveDirs(parser.AgentHax))
			assert.Equal(t, tc.config || tc.override, cfg.IsUserConfigured(parser.AgentHax))
			path := filepath.Join(want, "example.1234", "2026-10-01T12-00-00Z_12345678-1234-4234-8234-123456789abc.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(`{"type":"session","version":1,"hax_version":"0.6.0","id":"12345678-1234-4234-8234-123456789abc"}`+"\n"), 0o600))
			provider, ok := parser.NewProvider(parser.AgentHax, parser.ProviderConfig{Roots: cfg.ResolveDirs(parser.AgentHax)})
			require.True(t, ok)
			sources, err := provider.Discover(t.Context())
			require.NoError(t, err)
			require.Len(t, sources, 1)
			assert.Equal(t, path, sources[0].DisplayPath)
		})
	}
}
