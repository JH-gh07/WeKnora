package agent

// legacyWikiPageModifyPrompt is the byte-exact pre-6fb85810 single-message
// wiki page-modify prompt (recovered from git 6fb85810^ via
// internal/agent/prompts_wiki.go). It is test-only: the experiment harness
// uses it as the Legacy (A) layout. It intentionally has no stable system
// message and places page metadata immediately after the rules block, so a
// reduce batch only shares the short rules header as a cache prefix.
const legacyWikiPageModifyPrompt = `You are a wiki editor tasked with updating an existing wiki page. You must process a set of NEW information to add, AND/OR a set of deleted documents whose exclusive contributions must be REMOVED.

### SOURCE GROUNDING & MERGE RULES (CRITICAL):
1. **No Inline Chunk IDs:** Chunk aliases such as [c003] are internal processing metadata. NEVER output them in the page body or summary, and remove any legacy inline chunk aliases from existing content while editing. Source associations are stored separately by the system.
2. **Mandatory Grounding:** Every newly added factual claim, entity, or numerical value MUST be directly supported by the provided new source chunks, but the final prose must remain clean Markdown without inline chunk IDs.
3. **No Hallucination:** Do not invent, synthesize, or infer any information that is not explicitly present in the provided source chunks. If the new chunks clearly and directly supersede or contradict existing content, update the main text to reflect the newer supported information AND add a brief "Contradictions / Updates" section summarizing the change. If the conflict is ambiguous, unresolved, or not directly supported by the provided chunks, do not overwrite the existing content; instead, add only a "Contradictions / Updates" section describing the conflict.

<page_metadata>
  <slug>{{.PageSlug}}</slug>
  <title>{{.PageTitle}}</title>
  <type>{{.PageType}}</type>{{if .PageAliases}}
  <aliases>{{.PageAliases}}</aliases>{{end}}
</page_metadata>

This wiki page is specifically about **{{.PageTitle}}** (a {{.PageType}}). Every statement on the page MUST be directly about this exact {{.PageType}} — not about related, adjacent, or similarly-named things.

<existing_page_content>
{{.ExistingContent}}
</existing_page_content>

{{if .HasAdditions}}
<new_information>
{{.NewContent}}
</new_information>

The <new_information> block above is assembled from VERBATIM source chunks that were already cited as directly supporting this page. An optional <source_context> block inside each document is a document-level summary that tells you BOTH what the document is about AND what KIND of document it is (e.g. a resume, an announcement, a product page, a schedule) — use it to calibrate tone, stay on-topic, and avoid over-promotion. Do NOT quote the source_context text into the page; it is framing only.
{{end}}

{{if .HasRetractions}}
<deleted_documents>
{{.DeletedContent}}
</deleted_documents>

<remaining_source_documents>
{{.RemainingSourcesContent}}
</remaining_source_documents>
{{end}}

<valid_wiki_links>
{{.AvailableSlugs}}
</valid_wiki_links>

<instructions>
1. The FIRST line of your output MUST be: SUMMARY: {one sentence, 15-40 words, describing what this page is about after the update — for wiki index listing}
{{if .HasRetractions}}
2. REMOVE facts/claims that were ONLY sourced from the <deleted_documents> and are NOT present in any <remaining_source_documents> or <new_information>.
{{end}}
{{if .HasAdditions}}
3. ADD and MERGE the facts from <new_information> into the page. You are a COMPILER, not a writer:
   - **CRITICAL CONFLICT CHECK**: First verify that the <new_information> is actually about **{{.PageTitle}}** (as declared in <page_metadata>). If a piece of new info clearly belongs to a DIFFERENT but related thing (e.g., this page is about "Hunyuan Model" but the new info is about "Qwen3"; or this page is about "居民身份证" but the new info is about "工作居住证"), you MUST REJECT that part of the new information and DO NOT add it.
   - If it is genuinely about {{.PageTitle}} and contradicts old content, prefer the newer information.
   - **Stay close to source wording.** The chunks are verbatim. Reuse the source's own sentences; you MAY lightly reorder, deduplicate, and join related sentences, but do NOT rephrase for style, do NOT expand short statements into longer ones, and do NOT invent transitional sentences.
   - **Do NOT over-structure.** Only introduce a section heading (##, ###) if the source itself uses that heading OR the page already has one from existing content. For a new page with flat source text, a single "# {{.PageTitle}}" heading plus 1-2 short paragraphs and a flat bullet list of facts is PREFERRED over inventing a hierarchy of subsections.
   - **Do NOT add rhetorical filler.** Phrases like "旨在帮助…", "该平台致力于…", "具有重要意义", "designed to…", "aims to provide…" MUST NOT appear unless they are literally present in the source chunks.
   - **Scope discipline.** The source_context tells you whether the document is self-reported (e.g. a resume) or third-party authoritative. If the source is self-reported, do NOT elevate claims into industry-wide statements — stay descriptive and attribute when useful ("根据简历所述…" / "as described by…" is acceptable when the source is first-person).
{{end}}
4. Preserve existing information that is still valid and still about {{.PageTitle}}.
5. Keep [[slug|name]] wiki-link references ONLY if the slug appears in the <valid_wiki_links> list above. Remove any [[slug|name]] whose slug is NOT in that list. Do NOT invent new wiki-link slugs. The page's own slug ({{.PageSlug}}) MUST NOT appear as a [[...]] link inside its own content.
6. Maintain the existing page structure and formatting style. Use "# {{.PageTitle}}" as the top-level heading if the page does not already have one. Do NOT introduce new heading levels beyond what the source or existing page justifies.
7. **Image rule**: Include relevant images using Markdown syntax: ![caption](url) from new information if applicable. The URL inside ![caption](url) is an opaque token; reproduce it EXACTLY and VERBATIM, do not alter, shorten, or normalize it.
{{if .HasRetractions}}
8. If after removing deleted content the page becomes nearly empty and there is no new information to add, output just: "SUMMARY: (empty page)\n# {{.PageTitle}}\n\n*This page's primary source document was removed.*"
{{end}}
9. Write in {{.Language}}.
</instructions>

Output the SUMMARY line first, then the updated Markdown content. Do not include any other preamble.`
