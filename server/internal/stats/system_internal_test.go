package stats

import "testing"

func TestSystemSlugVocabulary(t *testing.T) {
	const hash = "01kittensol"
	cases := []struct {
		name string
		want string
	}{
		{"Solar System (Dense)", "solar-system-dense"},
		{"  punctuation...and___spaces  ", "punctuation-and-spaces"},
		{"☄", hash[:8]},
	}
	for _, tc := range cases {
		if got := systemSlug(tc.name, hash); got != tc.want {
			t.Errorf("systemSlug(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}

	got := systemSlug("ABCDEFGHIJKLMNOPQRSTUVWXYZ abcdefghijklmnopqrstuvwxyz 0123456789 tail", hash)
	if len(got) != systemSlugMax {
		t.Fatalf("long slug length = %d, want %d: %q", len(got), systemSlugMax, got)
	}
	if got != "abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstu" {
		t.Fatalf("long slug = %q", got)
	}
}
