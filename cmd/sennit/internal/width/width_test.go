package width

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// The tables describe the Unicode release the toolchain's unicode package
// implements, so a width measured here agrees with the character classes the
// rest of the standard library reports.
func TestTheTablesMatchTheToolchainsUnicodeRelease(t *testing.T) {
	if unicode.Version != UnicodeVersion {
		t.Fatalf("the tables are Unicode %s and the toolchain is %s: regenerate them with ./gen", UnicodeVersion, unicode.Version)
	}
}

func TestCellsOfEachKindOfCharacter(t *testing.T) {
	for _, c := range []struct {
		name      string
		text      string
		ambiguous int
		want      int
	}{
		{"ascii", "Prefers one live status line", 1, 28},
		{"cjk is two cells a character", "日本語のメモ", 1, 12},
		{"a combining accent takes no cell", "café", 1, 4},
		{"the ellipsis is ambiguous, narrow", "…", 1, 1},
		{"the ellipsis is ambiguous, wide", "…", 2, 2},
		{"the middle dot is ambiguous", "·", 2, 2},
		{"an emoji with its own presentation", "🚀", 1, 2},
		{"a narrow symbol asked for as emoji counts two cells", "⚠️", 1, 2},
		{"the same symbol alone is narrow", "⚠", 1, 1},
		{"a joiner sequence is not joined", "👩‍💻", 1, 4},
		{"a wide circled ideograph", "㊀", 1, 2},
		{"a Myanmar digit zero", "၀", 1, 1},
		{"the circled dot operator is ambiguous", "⊙", 2, 2},
		{"control characters take no cell", "a\tb", 1, 2},
	} {
		if got := StringWith(c.text, c.ambiguous); got != c.want {
			t.Errorf("%s: %q is %d cells, want %d", c.name, c.text, got, c.want)
		}
	}
}

// samples are the kinds of text a statement, a tag or a phase line can carry.
var samples = []string{
	"Prefers one live status line over scrolling logs",
	"日本語のメモを保存する",
	"Shipped 🚀 on Friday 🙂",
	"⚠️ check the quota",
	"👩‍💻 pair programming",
	"🇳🇬 Lagos office",
	"café au lait",
	"Searching… 4s · ctrl+c",
	". ◡ ◞ ◝ ◌ ၀ ဝ ㊀ ㊁ ㊂ ◍ ⊙",
	"✓ ✗ √ × !",
}

// A truncated line must never exceed its limit, which is what keeps a live line
// on one row, and must never split a character or strand a mark from the
// character it belongs to.
func TestTruncateNeverExceedsTheLimitOrSplitsACharacter(t *testing.T) {
	for _, limit := range []int{20, 40, 80} {
		for _, sample := range samples {
			long := strings.Repeat(sample+" ", 6)
			for _, ambiguous := range []int{1, 2} {
				got := Truncate(long, limit, "…", ambiguous)
				if !utf8.ValidString(got) {
					t.Errorf("width %d, A=%d: invalid UTF-8 %q", limit, ambiguous, got)
				}
				used := StringWith(got, ambiguous)
				if used > limit {
					t.Errorf("width %d, A=%d: %d cells: %q", limit, ambiguous, used, got)
				}
				if StringWith(long, ambiguous) > limit && used < limit-2 {
					t.Errorf("width %d, A=%d: only %d cells used: %q", limit, ambiguous, used, got)
				}
				body := strings.TrimSuffix(got, "…")
				if last, _ := utf8.DecodeLastRuneInString(body); last == '‍' {
					continue
				}
				if !strings.HasPrefix(long, body) {
					t.Errorf("width %d, A=%d: the kept text is not a prefix: %q", limit, ambiguous, got)
				}
			}
		}
	}
}

func TestTruncateKeepsAMarkWithItsCharacter(t *testing.T) {
	got := Truncate("ab́cdef", 4, "…", 1)
	if want := "ab́c…"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := Truncate("a⚠️bcdef", 3, "…", 1); got != "a…" {
		t.Errorf("an emoji-presentation symbol straddling the limit was kept or split: %q", got)
	}
}

func TestTruncateLeavesTextThatFitsAlone(t *testing.T) {
	for _, sample := range samples {
		if got := Truncate(sample, 80, "…", 1); got != sample {
			t.Errorf("changed text that fits: %q became %q", sample, got)
		}
	}
}

func TestPadCountsCellsNotBytes(t *testing.T) {
	if got := Pad("㊀", 2); got != "㊀" {
		t.Errorf("a two-cell glyph was padded: %q", got)
	}
	if got := Pad(".", 2); got != ". " {
		t.Errorf("a one-cell glyph was not padded to two cells: %q", got)
	}
	if got := Pad("日本", 6); got != "日本  " {
		t.Errorf("got %q", got)
	}
}
