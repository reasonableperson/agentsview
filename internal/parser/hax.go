package parser

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"

	"github.com/tidwall/gjson"
	"go.kenn.io/agentsview/internal/money"
)

type haxItem struct {
	value                   gjson.Result
	line                    int
	model, provider, effort string
}

func parseHaxSession(ctx context.Context, path, machine string) (*ParsedSession, []ParsedMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	reader := newLineReaderContext(ctx, f, maxLineSize)
	defer releaseLineReader(reader)
	line, ok := reader.next()
	if reader.skippedOversized {
		return nil, nil, fmt.Errorf("hax record exceeds %d bytes", maxLineSize)
	}
	if !ok {
		return nil, nil, reader.Err()
	}
	if !gjson.Valid(line) {
		return nil, nil, fmt.Errorf("invalid hax session header")
	}
	header := gjson.Parse(line)
	if header.Get("type").Str != "session" || !header.Get("hax_version").Exists() {
		return nil, nil, nil
	}
	if header.Get("version").Int() != 1 || !IsValidSessionID(header.Get("id").Str) {
		return nil, nil, fmt.Errorf("unsupported hax session version or invalid session ID")
	}
	cwd := header.Get("cwd").Str
	session := &ParsedSession{
		ID:              "hax:" + header.Get("id").Str,
		SourceSessionID: header.Get("id").Str,
		SourceVersion:   header.Get("hax_version").Str,
		Agent:           AgentHax,
		Machine:         machine,
		Cwd:             cwd,
		GitBranch:       header.Get("git_branch").Str,
		Project:         ExtractProjectFromCwdWithBranchContext(ctx, cwd, header.Get("git_branch").Str),
		StartedAt:       parseTimestamp(header.Get("timestamp").Str),
		File:            FileInfo{Path: path, Size: info.Size(), Mtime: info.ModTime().UnixNano()},
	}
	if parent := header.Get("forked_from").Str; parent != "" {
		session.ParentSessionID = "hax:" + parent
		session.RelationshipType = RelFork
	}
	model, provider, effort := header.Get("model").Str, header.Get("provider").Str, header.Get("effort").Str
	var items []haxItem
	for lineNumber := 2; ; lineNumber++ {
		line, ok := reader.next()
		if !ok {
			break
		}
		if !gjson.Valid(line) {
			session.MalformedLines++
			continue
		}
		value := gjson.Parse(line)
		switch value.Get("type").Str {
		case "selection":
			model, provider, effort = value.Get("model").Str, value.Get("provider").Str, value.Get("effort").Str
		case "undo":
			keep := value.Get("keep_user_turns")
			if keep.Type == gjson.Number && keep.Int() >= 0 && keep.Float() == float64(keep.Int()) {
				items = haxUndo(items, keep.Int())
			}
		}
		if value.Get("type").Exists() {
			continue
		}
		if value.Get("kind").Str == "turn_usage" && !value.Get("inherited").Bool() {
			// Undo changes the live transcript, not the requests already billed.
			haxAppendUsage(session, value, lineNumber, model, provider)
		}
		if value.Get("kind").Str != "" {
			items = append(items, haxItem{value: value, line: lineNumber, model: model, provider: provider, effort: effort})
		}
	}
	if err := reader.Err(); err != nil {
		return nil, nil, err
	}
	if reader.skippedOversized {
		return nil, nil, fmt.Errorf("hax record exceeds %d bytes", maxLineSize)
	}
	messages, err := decodeHaxMessages(ctx, items)
	if err != nil {
		return nil, nil, err
	}
	session.FirstMessage, session.UserMessageCount, err = firstMessageAndUserCountContext(ctx, messages)
	if err != nil {
		return nil, nil, err
	}
	session.SessionName = session.FirstMessage
	session.MessageCount = len(messages)
	return session, messages, nil
}

func haxSyntheticOrigin(origin string) bool {
	switch origin {
	case "compact_seed", "continuation", "interrupted", "skipped", "refused", "summarized", "task_note", "compaction":
		return true
	default:
		return false
	}
}

func haxUndo(items []haxItem, keep int64) []haxItem {
	var prompts int64
	for i, item := range items {
		if item.value.Get("kind").Str != "user" || haxSyntheticOrigin(item.value.Get("origin").Str) {
			continue
		}
		if prompts == keep {
			if i > 0 && items[i-1].value.Get("kind").Str == "turn_boundary" {
				i--
			}
			return items[:i]
		}
		prompts++
	}
	return items
}

func decodeHaxMessages(ctx context.Context, items []haxItem) ([]ParsedMessage, error) {
	var messages []ParsedMessage
	for i, item := range items {
		if err := contextErrEvery(ctx, i); err != nil {
			return nil, err
		}
		value := item.value
		kind := value.Get("kind").Str
		message := ParsedMessage{
			Ordinal:         len(messages),
			Model:           item.model,
			ProviderID:      item.provider,
			ReasoningEffort: item.effort,
			SourceType:      kind,
			SourceSubtype:   value.Get("origin").Str,
			SourceUUID:      fmt.Sprintf("line-%d", item.line),
			IsSystem:        haxSyntheticOrigin(value.Get("origin").Str),
		}
		switch kind {
		case "user":
			message.Role = RoleUser
			message.Content = value.Get("text").Str
			message.IsCompactBoundary = value.Get("origin").Str == "compact_seed"
		case "assistant":
			message.Role = RoleAssistant
			message.Content = value.Get("text").Str
		case "reasoning":
			message.Role = RoleAssistant
			message.HasThinking = true
			message.ThinkingText = value.Get("reasoning_text").Str
			if message.ThinkingText != "" {
				message.Content = "[Thinking]\n" + message.ThinkingText + "\n[/Thinking]"
			}
		case "tool_call":
			message.Role = RoleAssistant
			message.HasToolUse = true
			name := value.Get("tool_name").Str
			input := value.Get("arguments").Str
			message.ToolCalls = []ParsedToolCall{{
				ToolUseID: value.Get("call_id").Str,
				ToolName:  name,
				Category:  NormalizeToolCategory(name),
				InputJSON: input,
			}}
			message.Content = "[Tool: " + name + "]\n" + input
		case "tool_result":
			message.Role = RoleUser
			output := value.Get("output").Str
			encoded, err := json.Marshal(output)
			if err != nil {
				return nil, err
			}
			message.ToolResults = []ParsedToolResult{{
				ToolUseID:     value.Get("call_id").Str,
				ContentRaw:    string(encoded),
				ContentLength: len(output),
			}}
			message.ContentLength = len(output)
		default:
			continue
		}
		if kind != "tool_result" {
			message.ContentLength = len(message.Content)
		}
		messages = append(messages, message)
	}
	return messages, ctx.Err()
}

func haxAppendUsage(session *ParsedSession, value gjson.Result, line int, model, provider string) {
	usage := value.Get("usage")
	if !usage.IsObject() {
		return
	}
	if value.Get("model").Str != "" {
		model = value.Get("model").Str
	}
	if value.Get("provider").Str != "" {
		provider = value.Get("provider").Str
	}
	cached := max(int(usage.Get("cached").Int()), 0)
	written := max(int(usage.Get("cache_write").Int()), 0)
	input := max(int(usage.Get("input").Int())-cached-written, 0)
	if uncached := usage.Get("in_tokens"); uncached.Exists() && uncached.Type == gjson.Number && uncached.Int() >= 0 {
		input = int(uncached.Int())
	}
	event := ParsedUsageEvent{
		SessionID:                session.ID,
		Source:                   "hax",
		Model:                    model,
		ProviderID:               provider,
		InputTokens:              input,
		OutputTokens:             max(int(usage.Get("output").Int()), 0),
		CacheReadInputTokens:     cached,
		CacheCreationInputTokens: written,
		DedupKey:                 fmt.Sprintf("%s:usage:%d", session.ID, line),
		// Hax records a session start, but no per-request wall-clock timestamp.
		OccurredAt: timeString(session.StartedAt, session.EndedAt),
	}
	cost := usage.Get("cost_total")
	if !cost.Exists() {
		cost = usage.Get("cost")
	}
	if cost.Type == gjson.Number && cost.Float() >= 0 {
		if amount, err := money.FromFloatDollars(cost.Float()); err == nil {
			event.Cost = &amount
			event.CostStatus = "actual"
			if usage.Get("cost_estimated").Bool() {
				event.CostStatus = "estimated"
			}
			event.CostSource = "hax"
		}
	}
	if usage.Get("output").Exists() {
		session.HasTotalOutputTokens = true
		session.TotalOutputTokens += event.OutputTokens
	}
	if usage.Get("input").Exists() {
		session.HasPeakContextTokens = true
		session.PeakContextTokens = max(session.PeakContextTokens, max(int(usage.Get("input").Int()), 0))
	}
	session.UsageEvents = append(session.UsageEvents, event)
}
