package web

import "testing"

func TestScopeLabelHasReaderLabelsAndFutureFallback(t *testing.T) {
	for key, want := range map[string]string{
		"player": "Players",
		"career": "Saves",
		"system": "Systems",
		"future": "future",
	} {
		if got := scopeLabel(key); got != want {
			t.Errorf("scopeLabel(%q) = %q, want %q", key, got, want)
		}
	}
}
