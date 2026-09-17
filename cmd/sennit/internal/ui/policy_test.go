package ui

import (
	"fmt"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// The whole matrix: each stream a terminal or not, NO_COLOR, FORCE_COLOR, both,
// TERM=dumb, every --color value and SENNIT_NO_PROGRESS.
func TestColorAndAnimationPolicyOverTheWholeMatrix(t *testing.T) {
	type environment struct {
		name   string
		values map[string]string
	}
	environments := []environment{
		{"nothing set", map[string]string{"LANG": "C.UTF-8"}},
		{"NO_COLOR", map[string]string{"NO_COLOR": "1"}},
		{"FORCE_COLOR", map[string]string{"FORCE_COLOR": "1"}},
		{"NO_COLOR and FORCE_COLOR", map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}},
		{"TERM=dumb", map[string]string{"TERM": "dumb"}},
		{"SENNIT_NO_PROGRESS", map[string]string{NoProgressEnv: "1"}},
	}
	for _, stdoutTerminal := range []bool{true, false} {
		for _, stderrTerminal := range []bool{true, false} {
			for _, e := range environments {
				for _, mode := range []ColorMode{ColorAuto, ColorAlways, ColorNever} {
					name := fmt.Sprintf("stdout %v stderr %v %s --color=%s", stdoutTerminal, stderrTerminal, e.name, mode)
					t.Run(name, func(t *testing.T) {
						got := Decide(mode, Probe{
							StdoutTerminal: stdoutTerminal, StderrTerminal: stderrTerminal, StderrEscapes: true,
							GOOS: "linux", Getenv: env(e.values),
						})
						want := func(terminal bool) bool {
							switch {
							case mode == ColorAlways:
								return true
							case mode == ColorNever:
								return false
							case e.values["NO_COLOR"] != "":
								return false
							case e.values["FORCE_COLOR"] != "":
								return true
							case e.values["TERM"] == "dumb":
								return false
							}
							return terminal
						}
						if got.StdoutColor != want(stdoutTerminal) {
							t.Errorf("stdout colour %v, want %v", got.StdoutColor, want(stdoutTerminal))
						}
						if got.StderrColor != want(stderrTerminal) {
							t.Errorf("stderr colour %v, want %v", got.StderrColor, want(stderrTerminal))
						}
						animate := stdoutTerminal && stderrTerminal && e.values[NoProgressEnv] == "" && e.values["TERM"] != "dumb"
						if got.Animate != animate {
							t.Errorf("animate %v, want %v", got.Animate, animate)
						}
					})
				}
			}
		}
	}
}

// NO_COLOR is about colour. A person who sets it still gets the live line.
func TestNoColorLeavesTheAnimationOn(t *testing.T) {
	got := Decide(ColorAuto, Probe{StdoutTerminal: true, StderrTerminal: true, StderrEscapes: true, GOOS: "linux",
		Getenv: env(map[string]string{"NO_COLOR": "1"})})
	if !got.Animate || got.StderrColor {
		t.Errorf("NO_COLOR: animate %v, colour %v; want animation without colour", got.Animate, got.StderrColor)
	}
}

func TestAConsoleThatRefusesEscapesIsNotAnimated(t *testing.T) {
	got := Decide(ColorAuto, Probe{StdoutTerminal: true, StderrTerminal: true, StderrEscapes: false, GOOS: "windows",
		Getenv: env(nil)})
	if got.Animate {
		t.Error("a console that cannot interpret escape sequences would print the live line as text")
	}
}

func TestGlyphTier(t *testing.T) {
	cases := []struct {
		name   string
		goos   string
		values map[string]string
		want   Tier
	}{
		{"a UTF-8 locale", "linux", map[string]string{"LANG": "en_US.UTF-8"}, TierFull},
		{"LC_ALL wins over LANG", "linux", map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, TierASCII},
		{"LC_CTYPE wins over LANG", "linux", map[string]string{"LC_CTYPE": "en_US.UTF-8", "LANG": "C"}, TierFull},
		{"an empty LC_ALL is unset", "linux", map[string]string{"LC_ALL": "", "LANG": "C.UTF-8"}, TierFull},
		{"a bare UTF-8 codeset", "darwin", map[string]string{"LC_CTYPE": "UTF-8"}, TierFull},
		{"no locale at all is the POSIX locale", "darwin", nil, TierASCII},
		{"the kernel console", "linux", map[string]string{"TERM": "linux", "LANG": "C.UTF-8"}, TierASCII},
		{"Windows Terminal", "windows", map[string]string{"WT_SESSION": "3a1c"}, TierFull},
		{"another Windows console", "windows", nil, TierBasic},
		{"SENNIT_GLYPHS overrides detection", "windows", map[string]string{GlyphsEnv: "full"}, TierFull},
		{"SENNIT_GLYPHS can ask for full without Unicode", "linux", map[string]string{GlyphsEnv: "full"}, TierFull},
		{"SENNIT_GLYPHS basic", "linux", map[string]string{GlyphsEnv: "basic", "LANG": "C.UTF-8"}, TierBasic},
		{"SENNIT_GLYPHS ascii", "linux", map[string]string{GlyphsEnv: "ascii", "LANG": "C.UTF-8"}, TierASCII},
		{"an unknown SENNIT_GLYPHS is ignored", "linux", map[string]string{GlyphsEnv: "fancy", "LANG": "C.UTF-8"}, TierFull},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(ColorAuto, Probe{GOOS: c.goos, Getenv: env(c.values)}).Tier
			if got != c.want {
				t.Errorf("tier %d, want %d", got, c.want)
			}
		})
	}
}

func TestUTF8LocaleNames(t *testing.T) {
	for name, want := range map[string]bool{
		"en_US.UTF-8": true, "C.UTF-8": true, "de_DE.utf8": true, "sr_RS.UTF-8@latin": true,
		"UTF-8": true, "utf8": true, "UTF-8@euro": true,
		"C": false, "POSIX": false, "en_US": false, "en_US.ISO8859-1": false, "": false, "UTF-16": false,
	} {
		if got := utf8Locale(name); got != want {
			t.Errorf("utf8Locale(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParseColorMode(t *testing.T) {
	for _, value := range []string{"auto", "always", "never"} {
		if _, ok := ParseColorMode(value); !ok {
			t.Errorf("%q refused", value)
		}
	}
	for _, value := range []string{"", "yes", "Always", "true"} {
		if _, ok := ParseColorMode(value); ok {
			t.Errorf("%q accepted", value)
		}
	}
}
