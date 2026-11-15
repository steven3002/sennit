package main

import (
	"errors"
	"fmt"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/vault"
)

// A problem is a failure with the message this command line shows for it.
//
// The rule it exists to keep is rustc's: the error says what is wrong and the
// hints say what to do about it, so a reader looking for the command to run
// does not have to find it inside a paragraph.
type problem struct {
	message ui.Message
	status  int
	err     error
}

func (p *problem) Error() string { return p.message.Text }
func (p *problem) Unwrap() error { return p.err }

// refuse states a problem in the command's own words.
func refuse(text string) *problem {
	return &problem{message: ui.Message{Kind: ui.KindError, Text: text}}
}

// because adds the paragraphs that explain why, which is where a reason or a
// reassurance about what was not lost belongs.
func (p *problem) because(explanation ...string) *problem {
	p.message.Explanation = append(p.message.Explanation, explanation...)
	return p
}

// try adds what to do about it.
func (p *problem) try(hints ...string) *problem {
	p.message.Hints = append(p.message.Hints, hints...)
	return p
}

// exit sets the status this failure ends with. The default is 1; a command
// that was not understood at all ends with 2, as it always has.
func (p *problem) exit(status int) *problem {
	p.status = status
	return p
}

// from carries the underlying error, so errors.Is still finds what happened.
func (p *problem) from(err error) *problem {
	p.err = err
	return p
}

// statusOf is the exit code a failure ends with.
func statusOf(err error) int {
	var p *problem
	if errors.As(err, &p) && p.status != 0 {
		return p.status
	}
	return 1
}

// messageFor turns an error into what a person is shown.
//
// The three failures a first run actually hits are answered with instructions
// rather than with the error a library produced, because each of them is a
// step the reader has not taken yet rather than a fault. Everything else is
// shown as the code stated it: a message this program cannot improve on is
// better passed through than paraphrased.
func messageFor(err error) ui.Message {
	var stated *problem
	if errors.As(err, &stated) {
		return stated.message
	}

	var indexer *sia.IndexerError
	switch {
	case keys.MissingAppKey(err):
		return ui.Message{
			Kind: ui.KindError,
			Text: "no Sia app key",
			Explanation: []string{"The indexer issues one when you approve this installation, and " +
				"`sennit connect` stores it for every later run. It is a secret: never pass it as an argument."},
			Hints: []string{
				"`sennit connect` approves this installation and stores the key",
				"or set " + keys.AppKeyEnv + " to a key that approval issued",
				"to work without an indexer, put `--offline` before the text: " +
					"`sennit remember --offline --context \"...\" \"...\"`",
			},
		}
	case keys.WrongAppKeyLength(err):
		return ui.Message{
			Kind: ui.KindError,
			Text: err.Error(),
			Explanation: []string{"The key in " + keys.AppKeyEnv + " arrived damaged, most likely " +
				"truncated by the copy or the secret store it came through."},
			Hints: []string{"set it to the whole value that approval issued, or remove it and run " +
				"`sennit connect` to store a new one"},
		}
	case errors.Is(err, keys.ErrNoPhrase):
		return ui.Message{
			Kind: ui.KindError,
			Text: "no recovery phrase",
			Explanation: []string{"Nothing supplied one: " + keys.PhraseEnv + " is not set, nothing came in " +
				"on stdin, and no profile is stored."},
			Hints: []string{
				"`sennit connect` stores it with the app key for every later run",
				"or set " + keys.PhraseEnv + ", or pipe the phrase in on stdin",
				"no phrase yet? `sennit init --new-phrase` prints one",
			},
		}
	case errors.As(err, &indexer):
		// The operation, the service and the status are the failure; the advice
		// is why and what to do, which reads as an explanation rather than as
		// part of the error.
		stripped := sia.IndexerError{Op: indexer.Op, Indexer: indexer.Indexer, Status: indexer.Status}
		message := ui.Message{Kind: ui.KindError, Text: stripped.Error()}
		if indexer.Advice != "" {
			message.Explanation = []string{indexer.Advice + "."}
		}
		return message
	}
	return ui.Message{Kind: ui.KindError, Text: err.Error()}
}

// needsIndexer is the refusal of a command that cannot work from this device.
//
// Being offline on purpose and an indexer that did not answer are different
// situations and get different messages: one is a flag to drop and the other is
// a network to fix, and the warning about the second is already on screen.
func needsIndexer(command string, v *vault.Vault, queued string) *problem {
	if v.OfflineBecause() != nil {
		p := refuse(command + " needs the indexer, and the indexer did not answer")
		if queued != "" {
			p.because(queued)
		}
		return p
	}
	p := refuse(command + " needs the indexer, and this run is offline")
	if queued != "" {
		p.because(queued)
	}
	return p.try("run it without `--offline`")
}

// degraded is the warning a vault prints when it wanted the indexer and did not
// get it: what still works, what is owed, and what to check.
func degraded(reason error, indexer string) ui.Message {
	return ui.Message{
		Kind: ui.KindWarning,
		Text: "the indexer did not answer, this run works from this device only",
		Explanation: []string{"Nothing was lost: reads are answered on this device, and anything " +
			"written is queued until a run reaches the indexer."},
		Hints: []string{"check that " + indexerName(reason, indexer) +
			" is the right address and that this machine has a network"},
	}
}

// cutCatalog is the warning a vault prints when it found the catalog's last
// change cut off by a crash and dropped it: what that undid, what still comes
// right on its own, and what has to be run again.
//
// The record cannot be named. The line that named it is the part nothing could
// read, so the warning says what each kind of change leaves behind instead.
func cutCatalog(changes int) ui.Message {
	text := "the catalog's last change was cut off by a crash and has been dropped"
	explanation := "It was one record's latest change, so the catalog is back to what it held for that " +
		"record before."
	if changes > 1 {
		text = fmt.Sprintf("%d changes to the catalog were cut off by crashes and have been dropped", changes)
		explanation = "Each was one record's latest change, so the catalog is back to what it held for " +
			"that record before."
	}
	return ui.Message{
		Kind: ui.KindWarning,
		Text: text,
		Explanation: []string{explanation + " A record that was being flushed is still queued, and a " +
			"later flush writes it again."},
		Hints: []string{"if a forget, a hydrate or a recover was running at the time, run it again"},
	}
}

// undatedListing is the warning a vault prints when it could not date its
// listing by the records it lists as it opened. It is one line: nothing was
// lost and nothing is asked of the reader, because the listing stays as it was
// and the next open tries again.
// folderOpenTo warns that the vault's folder was already there and other
// accounts can read it.
//
// Sennit's own files in it are this account's alone whatever the folder
// allows. The folder still shows anyone it lets in what is there and how large
// it is, and a file any other program puts there takes the folder's access. A
// folder Sennit did not make may be shared on purpose, so it is not changed.
func folderOpenTo(home, who, goos string) ui.Message {
	hint := "`chmod 700 " + posixWord(home) + "` makes it yours alone"
	if goos == "windows" {
		hint = "remove the other accounts from the folder's access list, under Security in its Properties"
	}
	return ui.Message{
		Kind: ui.KindWarning,
		Text: "the vault's folder can be read by other accounts on this machine",
		Data: []string{home},
		Explanation: []string{"It lets in " + who + ". Sennit keeps every file it makes there for this " +
			"account alone, but the folder shows what is there, and a file another program puts in it takes " +
			"the folder's access. Sennit did not make this folder, so it leaves it as it was set."},
		Hints: []string{hint},
	}
}

func undatedListing(reason error) ui.Message {
	return ui.Message{
		Kind: ui.KindWarning,
		Text: "the listing could not be dated by its records, and is tried again at the next open: " + reason.Error(),
	}
}

// indexerName is the address the failure names, which is the one that was
// actually tried, and the configured one when the failure does not say.
func indexerName(reason error, configured string) string {
	var indexer *sia.IndexerError
	if errors.As(reason, &indexer) && indexer.Indexer != "" {
		return indexer.Indexer
	}
	return configured
}
