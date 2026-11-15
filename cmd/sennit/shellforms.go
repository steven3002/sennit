package main

import (
	"strings"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/keys"
)

// A shellForm is one shell's way of writing a command, with the shell named so
// a reader can pick their own.
type shellForm struct {
	// Shell is empty where one form is all there is to show.
	Shell   string
	Command string
}

// loadFromFile is how each shell a user may be typing into puts a file's one
// line into an environment variable, without the value passing through the
// shell's history.
//
// A program cannot ask which shell started it. A POSIX shell exports SHELL.
// PowerShell and cmd set nothing that tells one from the other, and the tools
// that try anyway walk the list of running processes for a parent's name and
// fall back to a guess. A wrong guess here hands someone a command their shell
// rejects, straight after an approval they waited minutes for. So on Windows
// both of its shells are shown, each named, and a POSIX shell running there,
// such as Git Bash, is shown its own form as well.
func loadFromFile(goos, shell, variable, file string) []shellForm {
	posix := shellForm{Command: "export " + variable + "=$(cat " + posixWord(file) + ")"}
	if goos != "windows" {
		return []shellForm{posix}
	}
	forms := []shellForm{
		{Shell: "PowerShell", Command: "$env:" + variable + " = Get-Content " + powershellWord(file)},
		{Shell: "cmd", Command: "set /p " + variable + "=<" + cmdWord(file)},
	}
	if shell != "" {
		posix.Shell = shellName(shell)
		forms = append(forms, posix)
	}
	return forms
}

// keyHint is the advice sennit connect ends with: how to load the key it has
// just written.
//
// Where there is one form it stays the single line it has always been. Where
// there are several, each is a paragraph of its own under the advice, because
// a paragraph is wrapped without breaking the command in it, and a command cut
// to fit a narrow window is one nobody can run.
func keyHint(goos, shell, file string) ui.Message {
	const advice = "load the key into this shell without putting it in your history"
	forms := loadFromFile(goos, shell, keys.AppKeyEnv, file)
	if len(forms) == 1 {
		return ui.Message{Kind: ui.KindHint, Text: advice + ": `" + forms[0].Command + "`"}
	}
	hint := ui.Message{Kind: ui.KindHint, Text: advice + ", with the line for the shell you are in"}
	for _, form := range forms {
		hint.Explanation = append(hint.Explanation, form.Shell+": `"+form.Command+"`")
	}
	return hint
}

// shellName is the name a person knows a POSIX shell by, from the path in
// SHELL. Both separators are cut at, because Git Bash on Windows can report
// either.
func shellName(path string) string {
	name := path[strings.LastIndexAny(path, `/\`)+1:]
	name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	if name == "" {
		return "sh"
	}
	return name
}

// plainWord reports whether a file name can be written into a command as it
// stands in every shell here: nothing in it is a space, a quote, or a
// character one of the shells reads as its own.
func plainWord(word string, extra string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._-/"+extra, r):
		default:
			return false
		}
	}
	return true
}

// posixWord quotes a file name for a POSIX shell when it needs it. A backslash
// needs it: unquoted it escapes the next character, which turns a Windows path
// typed into Git Bash into a different path.
func posixWord(word string) string {
	if plainWord(word, ":+=@%,") {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// powershellWord quotes a file name for PowerShell when it needs it. Inside
// single quotes nothing is expanded, and a quote is written twice.
func powershellWord(word string) string {
	if plainWord(word, `\:`) {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", "''") + "'"
}

// cmdWord quotes a file name for cmd when it needs it. A file name on Windows
// cannot contain a double quote, so there is nothing inside to escape.
func cmdWord(word string) string {
	if plainWord(word, `\:`) {
		return word
	}
	return `"` + word + `"`
}
