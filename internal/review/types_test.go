package review

import "testing"

func TestFindingStateTextRoundTrip(t *testing.T) {
	states := []FindingState{
		StatePending, StateAccepted, StateRejected, StatePublished, StateFellBack, StateBelowThreshold,
	}
	for _, want := range states {
		t.Run(want.String(), func(t *testing.T) {
			text, err := want.MarshalText()
			if err != nil {
				t.Fatal(err)
			}
			var got FindingState
			if err := got.UnmarshalText(text); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("round-trip: %q → %v; want %v", text, got, want)
			}
		})
	}
	var s FindingState
	if err := s.UnmarshalText([]byte("bogus")); err == nil {
		t.Error("expected error for unknown state")
	}
}

func TestFindingBlocking(t *testing.T) {
	tests := []struct {
		name string
		f    Finding
		min  Severity
		want bool
	}{
		{"at threshold", Finding{Severity: SeverityMajor}, SeverityMajor, true},
		{"above threshold", Finding{Severity: SeverityCritical}, SeverityMajor, true},
		{"below threshold", Finding{Severity: SeverityMinor}, SeverityMajor, false},
		{"rejected never blocks", Finding{Severity: SeverityCritical, State: StateRejected}, SeverityMajor, false},
		{"published still blocks", Finding{Severity: SeverityCritical, State: StatePublished}, SeverityMajor, true},
		{"manual never blocks", Finding{Manual: true}, SeverityInfo, false},
		// A missing severity must not rank equal to info.
		{"no severity never blocks", Finding{}, SeverityInfo, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.Blocking(tt.min); got != tt.want {
				t.Errorf("Blocking(%s) = %v; want %v", tt.min, got, tt.want)
			}
		})
	}

	findings := []Finding{
		{Severity: SeverityCritical},
		{Severity: SeverityMajor, State: StateRejected},
		{Severity: SeverityInfo},
		{Manual: true},
	}
	if got := CountBlocking(findings, SeverityMajor); got != 1 {
		t.Errorf("CountBlocking = %d; want 1", got)
	}
}
