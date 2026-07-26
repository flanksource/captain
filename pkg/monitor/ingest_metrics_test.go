package monitor

import "testing"

func TestOfferRatioReportsHowMuchOfAParseIsRewritten(t *testing.T) {
	cases := []struct {
		name    string
		parsed  int64
		offered int64
		want    float64
	}{
		{name: "no ingest yet reports zero rather than dividing by zero"},
		{name: "first ingest of a file writes everything it parsed", parsed: 40, offered: 40, want: 1},
		{name: "append writes one line out of a long transcript", parsed: 4000, offered: 4, want: 0.001},
		{name: "re-parse with nothing new writes nothing", parsed: 4000, offered: 0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IngestStats{MessagesParsed: tc.parsed, MessagesOffered: tc.offered}.OfferRatio()
			if got != tc.want {
				t.Fatalf("OfferRatio() = %v, want %v", got, tc.want)
			}
		})
	}
}
