package agent

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// BuildWikiPageModifyMessages renders the production wiki page-modify prompt
// from the same template-data map generateWithTemplate consumes and assembles
// the production message layout:
//
//	system: WikiPageModifySystemPrompt (+ custom instructions)
//	user:   rendered WikiPageModifyUserPrompt (shared source context first)
//
// Task013 (real-provider Wiki prompt-cache A/B, gate AC-R8) freezes this
// function as the single production assembly seam: the experiment treatment
// arm must call it, and the experiment control arm must consume the same
// rendered sections so the two arms differ only in layout bundle. The caller
// (generateWithTemplate) must pass already-masked template data; no network,
// repository or provider dependency exists here.
//
// Changing block order, wording or message boundaries inside this function is
// a production prompt change and requires a Scope Change / re-preregistration,
// never a silent edit.
func BuildWikiPageModifyMessages(data map[string]string) ([]chat.Message, error) {
	tmpl, err := template.New("wiki_modify").Parse(WikiPageModifyUserPrompt)
	if err != nil {
		return nil, fmt.Errorf("parse wiki modify template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute wiki modify template: %w", err)
	}

	systemPrompt := types.AppendCustomPromptInstructions(
		WikiPageModifySystemPrompt,
		data["CustomInstructions"],
		data["InstructionScope"],
	)

	return []chat.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: buf.String()},
	}, nil
}
