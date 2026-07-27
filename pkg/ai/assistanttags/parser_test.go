package assistanttags

import (
	"reflect"
	"testing"
)

func TestParseStructuredAssistantMessage(t *testing.T) {
	input := `<proposed_plan>
# Ship it

- Add parser
</proposed_plan>

Source used: https://example.com/change

<oai-mem-citation>
<citation_entries>
MEMORY.md:10-12|note=[parser seam]
</citation_entries>
<rollout_ids>
019f3754-ecfa-7323-a76b-a0205ea30bbe
</rollout_ids>
</oai-mem-citation>`

	got := Parse(input)
	if len(got) != 3 {
		t.Fatalf("segments = %#v, want 3", got)
	}
	if got[0].Kind != SegmentPlan || got[0].Text != "# Ship it\n\n- Add parser" {
		t.Fatalf("plan = %#v", got[0])
	}
	if got[1].Kind != SegmentText || got[1].Text != "Source used: https://example.com/change" {
		t.Fatalf("text = %#v", got[1])
	}
	if got[2].Kind != SegmentMemoryCitation || got[2].Citation == nil {
		t.Fatalf("citation = %#v", got[2])
	}
	if !reflect.DeepEqual(got[2].Citation.CitationEntries, []string{"MEMORY.md:10-12|note=[parser seam]"}) {
		t.Fatalf("citation entries = %#v", got[2].Citation.CitationEntries)
	}
	if !reflect.DeepEqual(got[2].Citation.RolloutIDs, []string{"019f3754-ecfa-7323-a76b-a0205ea30bbe"}) {
		t.Fatalf("rollout ids = %#v", got[2].Citation.RolloutIDs)
	}
}

func TestParseLeavesUnknownAndMalformedXMLAsText(t *testing.T) {
	tests := []string{
		"<proposed_plan>missing close",
		"Before <proposed_plan>embedded</proposed_plan>",
		"```xml\n<proposed_plan>example</proposed_plan>\n```",
		"<thinking>legacy text</thinking>",
		"<oai-mem-citation><citation_entries>x</citation_entries></oai-mem-citation>",
	}
	for _, input := range tests {
		got := Parse(input)
		if len(got) != 1 || got[0].Kind != SegmentText || got[0].Text != input {
			t.Errorf("Parse(%q) = %#v, want unchanged text", input, got)
		}
	}
}

func TestParsePlanAndCitationWithoutAssistantText(t *testing.T) {
	input := `<proposed_plan>Do it</proposed_plan>
<oai-mem-citation>
<citation_entries>
</citation_entries>
<rollout_ids>
</rollout_ids>
</oai-mem-citation>`
	got := Parse(input)
	if len(got) != 2 || got[0].Kind != SegmentPlan || got[1].Kind != SegmentMemoryCitation {
		t.Fatalf("segments = %#v", got)
	}
}
