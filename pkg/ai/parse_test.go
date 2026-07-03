package ai

import (
	"fmt"
	"testing"
)

type verdict struct {
	Pass    bool   `json:"pass"`
	Comment string `json:"comment"`
}

func nonEmptyComment(v *verdict) error {
	if v.Comment == "" {
		return fmt.Errorf("comment is required")
	}
	return nil
}

func TestParseStructured(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		want    verdict
	}{
		{name: "bare json", raw: `{"pass":true,"comment":"ok"}`, want: verdict{Pass: true, Comment: "ok"}},
		{name: "fenced json", raw: "```json\n{\"pass\":false,\"comment\":\"nope\"}\n```", want: verdict{Comment: "nope"}},
		{name: "json envelope", raw: `{"result":"{\"pass\":true,\"comment\":\"wrapped\"}"}`, want: verdict{Pass: true, Comment: "wrapped"}},
		{name: "embedded in prose", raw: "Here is the result:\n{\"pass\":true,\"comment\":\"prose\"} — done", want: verdict{Pass: true, Comment: "prose"}},
		{name: "yaml body", raw: "pass: true\ncomment: yaml", want: verdict{Pass: true, Comment: "yaml"}},
		{name: "validation fails", raw: `{"pass":true,"comment":""}`, wantErr: true},
		{name: "garbage", raw: "not json at all", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseStructured(tc.raw, nonEmptyComment)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseStructured: %v", err)
			}
			if *got != tc.want {
				t.Errorf("got %+v, want %+v", *got, tc.want)
			}
		})
	}
}
