package main

import (
	"errors"

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
			Explanation: []string{"The indexer issues one when you approve this installation. " +
				"It is a secret: keep it out of shell history and never pass it as an argument."},
			Hints: []string{
				"set " + keys.AppKeyEnv + " to the key that approval issued",
				"not approved yet? `sennit connect --out sennit.key` issues one",
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
			Hints: []string{"set it to the whole value that approval issued, or issue a new one: " +
				"`sennit connect --out sennit.key`"},
		}
	case errors.Is(err, keys.ErrNoPhrase):
		return ui.Message{
			Kind:        ui.KindError,
			Text:        "no recovery phrase",
			Explanation: []string{"Sennit derives the vault's keys from the phrase on every run and never stores it."},
			Hints: []string{
				"set " + keys.PhraseEnv + ", or pipe the phrase in on stdin",
				"no phrase yet? `sennit init --new-phrase` prints one and stores nothing",
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

// indexerName is the address the failure names, which is the one that was
// actually tried, and the configured one when the failure does not say.
func indexerName(reason error, configured string) string {
	var indexer *sia.IndexerError
	if errors.As(reason, &indexer) && indexer.Indexer != "" {
		return indexer.Indexer
	}
	return configured
}
