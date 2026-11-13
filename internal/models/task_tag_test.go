package models

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeTaskTag(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {"  ", ""}, {" Feature ", "feature"}, {"BUG", "bug"},
		{" Docs/Security ", "docs/security"}, {"Release Notes", "release notes"},
		{"文档", "文档"}, {strings.Repeat("界", 64), strings.Repeat("界", 64)},
		{"<script>", "<script>"},
	} {
		got, err := NormalizeTaskTag(tc.input)
		if err != nil || string(got) != tc.want {
			t.Errorf("NormalizeTaskTag(%q) = %q, %v", tc.input, got, err)
		}
	}
	for _, input := range []string{strings.Repeat("界", 65), "docs\n", "\tdocs", "a\x00b", "a\u202eb", string([]byte{0xff})} {
		if _, err := NormalizeTaskTag(input); !errors.Is(err, ErrInvalidTaskTag) {
			t.Errorf("accepted %q: %v", input, err)
		}
	}
}
