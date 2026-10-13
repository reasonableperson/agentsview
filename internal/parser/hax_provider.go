package parser

import (
	"context"
	"path/filepath"
	"strings"
)

func newHaxProviderFactory(def AgentDef) ProviderFactory {
	caps := Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			SessionName:          CapabilitySupported,
			Cwd:                  CapabilitySupported,
			GitBranch:            CapabilitySupported,
			Relationships:        CapabilitySupported,
			Thinking:             CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			AggregateUsageEvents: CapabilitySupported,
			MalformedLineCount:   CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
	caps.Source.ForceReplaceOnParse = CapabilitySupported
	return NewSourceSetFactory(def, caps, func(cfg ProviderConfig) SourceSet {
		return NewDirectoryJSONLSourceSet(AgentHax, cfg.Roots,
			WithProjectHint(func(root, path string) string { return "" }),
			WithSessionIDFromPath(func(root, path string) string {
				name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
				_, id, ok := strings.Cut(name, "_")
				if !ok || !IsValidSessionID(id) {
					return ""
				}
				return id
			}),
			WithParseFile(func(ctx context.Context, path string, req ParseRequest) ([]ParseResult, []string, error) {
				session, messages, err := parseHaxSession(ctx, path, req.Machine)
				if err != nil || session == nil {
					return nil, nil, err
				}
				session.File.Hash = req.Fingerprint.Hash
				return []ParseResult{{Session: *session, Messages: messages, UsageEvents: session.UsageEvents}}, nil, nil
			}),
			WithForceReplace(),
		)
	})
}
