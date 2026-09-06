package main

// Task013 semantic section engine: splits the production-rendered treatment
// messages into canonical sections and assembles the same-content
// non-cache-friendly control layout. The splitter asserts template-structure
// invariants so any production template change fails loudly (drift detection),
// and the multiset verifier enforces plan §4.3 (only sequence/message-boundary
// may differ between arms).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
)

// Canonical section IDs, treatment order (plan §4.1/§4.3).
const (
	SecSystemRules     = "system_rules"
	SecSharedSourceCtx = "shared_source_contexts"
	SecPageMetadata    = "page_metadata"
	SecAboutLine       = "about_line"
	SecExistingContent = "existing_content"
	SecNewInformation  = "new_information"
	SecNewInfoFraming  = "new_info_framing"
	SecValidWikiLinks  = "valid_wiki_links"
	SecInstructions    = "instructions"
	SecOutputFormat    = "output_format"
)

// tagBlocks maps an opening tag to its canonical section ID, in treatment
// order. The P0 protocol covers the HasAdditions=true / HasRetractions=false
// branch only (plan §7.1; the retraction branch requires a separate protocol).
var tagBlocks = []struct {
	Tag string
	ID  string
}{
	{"<shared_source_contexts>", SecSharedSourceCtx},
	{"<page_metadata>", SecPageMetadata},
	{"<existing_page_content>", SecExistingContent},
	{"<new_information>", SecNewInformation},
	{"<valid_wiki_links>", SecValidWikiLinks},
	{"<instructions>", SecInstructions},
}

// gapSections maps the whitespace gap between tag block i and the next marker
// to a section ID. prefix is the EXACT leading whitespace the frozen template
// produces there (template-directive lines make some gaps "\n\n\n"); an entry
// with id "" is a separator-only gap and must equal prefix exactly.
var gapSections = []struct {
	id     string
	prefix string
}{
	{"", "\n\n\n"},              // shared → page_metadata ({{end}} line)
	{SecAboutLine, "\n\n"},      // page_metadata → about paragraph
	{"", "\n\n\n"},              // existing → new_information ({{if}} line)
	{SecNewInfoFraming, "\n\n"}, // new_information → framing paragraph
	{"", "\n\n"},                // valid_links → instructions
	{SecOutputFormat, "\n\n"},   // trailing text after the last tag
}

type section struct {
	id      string
	content string
}

// splitUser partitions the rendered user message into tag blocks and gap
// strings. It returns the tag-block sections in order, the derived gap
// sections (empty-gap entries skipped), and the raw gap strings for the
// lossless rejoin check.
func splitUser(user string) (blocks []section, gapSects []section, gaps []string, err error) {
	pos := 0
	for i, tb := range tagBlocks {
		start := strings.Index(user[pos:], tb.Tag)
		if start != 0 {
			return nil, nil, nil, fmt.Errorf("template drift: expected %q at offset %d of user message, got %d", tb.Tag, pos, start+pos)
		}
		closeTag := "</" + tb.Tag[1:]
		endRel := strings.Index(user[pos+len(tb.Tag):], closeTag)
		if endRel < 0 {
			return nil, nil, nil, fmt.Errorf("template drift: missing %q close tag", closeTag)
		}
		blockEnd := pos + len(tb.Tag) + endRel + len(closeTag)
		blocks = append(blocks, section{id: tb.ID, content: user[pos:blockEnd]})
		pos = blockEnd
		var gap string
		if i+1 < len(tagBlocks) {
			nextStart := strings.Index(user[pos:], tagBlocks[i+1].Tag)
			if nextStart < 0 {
				return nil, nil, nil, fmt.Errorf("template drift: missing %q after %q", tagBlocks[i+1].Tag, tb.Tag)
			}
			gap = user[pos : pos+nextStart]
			pos += nextStart
		} else {
			gap = user[pos:]
			pos = len(user)
		}
		spec := gapSections[i]
		if !strings.HasPrefix(gap, spec.prefix) {
			return nil, nil, nil, fmt.Errorf("template drift: gap after %q lacks expected prefix %q (%q)", tb.ID, spec.prefix, gap)
		}
		gaps = append(gaps, gap)
		if spec.id != "" {
			trimmed := strings.TrimSuffix(strings.TrimPrefix(gap, spec.prefix), "\n\n")
			if trimmed == "" {
				return nil, nil, nil, fmt.Errorf("template drift: gap section %q empty", spec.id)
			}
			gapSects = append(gapSects, section{id: spec.id, content: trimmed})
		} else if gap != spec.prefix {
			return nil, nil, nil, fmt.Errorf("template drift: separator gap after %q is %q, want %q", tb.ID, gap, spec.prefix)
		}
	}
	if pos != len(user) {
		return nil, nil, nil, fmt.Errorf("template drift: %d trailing bytes after last section", len(user)-pos)
	}
	return blocks, gapSects, gaps, nil
}

// treatmentSections renders the production messages (via the production
// builder) and returns the canonical sections. system_rules carries the entire
// production system message. The lossless partition check re-joins tag blocks
// with the recorded template-native gap strings and must reproduce the user
// message byte for byte.
func treatmentSections(system, user string) ([]section, error) {
	blocks, gapSects, gaps, err := splitUser(user)
	if err != nil {
		return nil, err
	}
	var rejoined strings.Builder
	for i, b := range blocks {
		rejoined.WriteString(b.content)
		if i < len(gaps) {
			rejoined.WriteString(gaps[i])
		}
	}
	if rejoined.String() != user {
		return nil, fmt.Errorf("split not lossless: rejoin diverges from production user message")
	}
	// Canonical treatment sequence: system_rules, then tag blocks and gap
	// sections interleaved in template order.
	out := []section{{id: SecSystemRules, content: system}}
	gi := 0
	for i, b := range blocks {
		out = append(out, b)
		if spec := gapSections[i]; spec.id != "" {
			out = append(out, gapSects[gi])
			gi++
		}
	}
	return out, nil
}

func sectionContents(ss []section) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.content
	}
	return out
}

// controlOrder is the frozen variable-first order (plan §4.2): page variables
// first, then the same stable rules/shared context, then the remaining task
// data. Control is a single user message.
var controlOrder = []string{
	SecPageMetadata, SecAboutLine, SecExistingContent, SecNewInformation,
	SecNewInfoFraming, SecSystemRules, SecSharedSourceCtx, SecValidWikiLinks,
	SecInstructions, SecOutputFormat,
}

// controlMessages assembles the control arm from the same canonical sections:
// one user message joining the sections in controlOrder with "\n\n".
func controlMessages(sections []section) ([]chat.Message, error) {
	byID := make(map[string]string, len(sections))
	for _, s := range sections {
		if _, dup := byID[s.id]; dup {
			return nil, fmt.Errorf("duplicate section %q", s.id)
		}
		byID[s.id] = s.content
	}
	if len(byID) != len(controlOrder) {
		return nil, fmt.Errorf("section count %d != control order %d", len(byID), len(controlOrder))
	}
	parts := make([]string, 0, len(controlOrder))
	for _, id := range controlOrder {
		c, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("control assembly missing section %q", id)
		}
		parts = append(parts, c)
	}
	return []chat.Message{{Role: "user", Content: strings.Join(parts, "\n\n")}}, nil
}

// controlSections returns the control arm's section view: same contents as the
// treatment sections, sequenced in controlOrder.
func controlSections(treat []section) ([]section, error) {
	byID := make(map[string]string, len(treat))
	for _, s := range treat {
		byID[s.id] = s.content
	}
	out := make([]section, 0, len(controlOrder))
	for _, id := range controlOrder {
		c, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("control missing section %q", id)
		}
		out = append(out, section{id: id, content: c})
	}
	return out, nil
}

// multisetKey computes a sequence-order-independent identity over sections.
func multisetKey(sections []section) string {
	items := make([]string, 0, len(sections))
	for _, s := range sections {
		items = append(items, s.id+"\x00"+s.content)
	}
	sort.Strings(items)
	return strings.Join(items, "\x1e")
}

// verifyMultiset enforces plan §4.3 hard conditions: every section's hash,
// byte length and occurrence count are identical between arms; only sequence
// and message boundary may differ.
func verifyMultiset(treat, ctrl []section) error {
	if multisetKey(treat) != multisetKey(ctrl) {
		return fmt.Errorf("NOT_COMPARABLE_LAYOUT: control/treatment section multiset differs")
	}
	type row struct {
		hash string
		n    int
		occ  int
	}
	index := func(ss []section) map[string]row {
		m := map[string]row{}
		for _, s := range ss {
			sum := sha256.Sum256([]byte(s.content))
			r := m[s.id]
			r.hash = hex.EncodeToString(sum[:])
			r.n = len(s.content)
			r.occ++
			m[s.id] = r
		}
		return m
	}
	a, b := index(treat), index(ctrl)
	if len(a) != len(b) {
		return fmt.Errorf("NOT_COMPARABLE_LAYOUT: section id sets differ (%d vs %d)", len(a), len(b))
	}
	for id, ra := range a {
		rb, ok := b[id]
		if !ok || ra != rb {
			return fmt.Errorf("NOT_COMPARABLE_LAYOUT: section %q differs between arms", id)
		}
	}
	return nil
}
