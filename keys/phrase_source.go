package keys

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// PhraseEnv is the environment variable carrying the recovery phrase.
const PhraseEnv = "SENNIT_PHRASE"

// ErrNoPhrase reports that no recovery phrase was supplied.
var ErrNoPhrase = fmt.Errorf("no recovery phrase: set %s or pipe it on stdin", PhraseEnv)

// ReadPhrase takes the recovery phrase from the environment, falling back to
// stdin. The vault never persists it: holding it only for the lifetime of one
// command is what makes "keys never leave the device" enforceable rather than
// aspirational.
func ReadPhrase(stdin io.Reader) (string, error) {
	if phrase := strings.TrimSpace(os.Getenv(PhraseEnv)); phrase != "" {
		return phrase, nil
	}
	if stdin == nil || isTerminal(stdin) {
		return "", ErrNoPhrase
	}
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read recovery phrase: %w", err)
		}
		return "", ErrNoPhrase
	}
	phrase := strings.TrimSpace(scanner.Text())
	if phrase == "" {
		return "", ErrNoPhrase
	}
	return phrase, nil
}

// isTerminal reports whether the reader is an interactive terminal rather than
// a pipe or a file.
//
// A terminal is not a way of supplying a phrase, and reading from one was never
// a decision so much as what falls out of not asking. Nothing here turns echo
// off, so a phrase typed at it is printed to the screen and kept in scrollback;
// the read blocks with nothing prompted, so a first run looks like it has hung;
// and the interrupt that would end it is claimed by a signal handler that the
// blocked read never consults, so the terminal cannot be escaped. Refusing
// costs nothing: the supported paths are the environment and a pipe, and
// neither is a character device.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
