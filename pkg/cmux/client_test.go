package cmux

import "testing"

func TestParseSurfaces(t *testing.T) {
	data := []byte(`{
	  "windows": [{
	    "workspaces": [{
	      "title": "gavel-claude",
	      "panes": [{
	        "surfaces": [
	          {"id": "CD1AD43E-1", "title": "✳ Review gavel cmux prompt documentation", "tty": "ttys018", "type": "terminal"},
	          {"id": "2EDAFDF1-2", "title": "⠐ implement-captain-ps-command", "tty": "ttys017", "type": "terminal"}
	        ]
	      }]
	    }, {
	      "title": "tool-cli",
	      "panes": [{
	        "surfaces": [
	          {"id": "6E8CA224-3", "title": "tool-cli", "tty": "ttys000", "type": "terminal"},
	          {"id": "", "title": "skip-me", "tty": "", "type": "terminal"}
	        ]
	      }]
	    }]
	  }]
	}`)

	surfaces, err := parseSurfaces(data)
	if err != nil {
		t.Fatalf("parseSurfaces: %v", err)
	}
	if len(surfaces) != 3 {
		t.Fatalf("expected 3 surfaces, got %d", len(surfaces))
	}

	s := surfaces["CD1AD43E-1"]
	if s.Title != "Review gavel cmux prompt documentation" {
		t.Fatalf("glyph not stripped: %q", s.Title)
	}
	if s.Workspace != "gavel-claude" {
		t.Fatalf("workspace = %q", s.Workspace)
	}
	if surfaces["2EDAFDF1-2"].Title != "implement-captain-ps-command" {
		t.Fatalf("braille glyph not stripped: %q", surfaces["2EDAFDF1-2"].Title)
	}
	if surfaces["6E8CA224-3"].Workspace != "tool-cli" {
		t.Fatalf("workspace = %q", surfaces["6E8CA224-3"].Workspace)
	}
	if _, ok := surfaces[""]; ok {
		t.Fatal("empty-id surface should be skipped")
	}
}
