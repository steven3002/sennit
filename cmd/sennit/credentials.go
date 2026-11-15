package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/profile"
	"github.com/steven3002/sennit/vault"
)

// Where a run's phrase or key came from, besides an environment variable,
// which is named by the variable's own name.
const (
	fromProfile = "the default profile"
	fromStdin   = "stdin"
	fromPrompt  = "the prompt"
)

// credentialSources says where this run's phrase and app key came from.
type credentialSources struct {
	phrase, key string
}

// storedProfile is the default profile as this run found it.
type storedProfile struct {
	read    bool
	profile profile.Profile
	summary profile.Summary
	found   bool
	err     error
}

// savedProfile returns the profile sennit connect stored, or found false when
// there is none.
//
// It is read at most once a run, because reading it can mean asking the
// credential store, and the phrase and the key are asked for at different
// points of the same command.
func (s *session) savedProfile() (stored profile.Profile, summary profile.Summary, found bool, err error) {
	if s.profiles == nil {
		return profile.Profile{}, profile.Summary{}, false, nil
	}
	if !s.saved.read {
		p, sum, err := s.profiles.Load()
		s.saved = storedProfile{read: true, profile: p, summary: sum, found: err == nil}
		if err != nil && !errors.Is(err, profile.ErrNone) {
			s.saved.err = err
		}
	}
	if s.saved.err != nil {
		return profile.Profile{}, profile.Summary{}, false,
			fmt.Errorf("read the default profile that sennit connect stored: %w", s.saved.err)
	}
	return s.saved.profile, s.saved.summary, s.saved.found, nil
}

// appKey takes the app key from the environment, or from the default profile
// when the environment has none.
//
// A stored key comes with the indexer that issued it, since no other indexer
// honours it, unless this run named an indexer itself. A key from the
// environment that is not the stored one is used, and said out loud: a
// variable left over from an earlier connect would otherwise send this run to
// another account without a word.
func (f *vaultFlags) appKey(out *session) (keys.AppKey, error) {
	key, err := keys.AppKeyFromEnv()
	switch {
	case err == nil:
		out.sources.key = keys.AppKeyEnv
		if out.profiles != nil {
			if summary, err := out.profiles.Summary(); err == nil && summary.KeyID != profile.KeyID(key) {
				out.warn(keyOverridden())
			}
		}
		return key, nil
	case !keys.MissingAppKey(err):
		return nil, err
	}

	stored, _, found, loadErr := out.savedProfile()
	if loadErr != nil {
		return nil, loadErr
	}
	if !found {
		return nil, err
	}
	out.sources.key = fromProfile
	if !f.indexerChosen() {
		f.indexer = stored.Indexer
	}
	return stored.AppKey, nil
}

// indexerChosen reports whether this run named an indexer, with --indexer or
// SENNIT_INDEXER.
func (f *vaultFlags) indexerChosen() bool {
	if os.Getenv(vault.IndexerEnv) != "" {
		return true
	}
	chosen := false
	if f.set != nil {
		f.set.Visit(func(set *flag.Flag) {
			if set.Name == "indexer" {
				chosen = true
			}
		})
	}
	return chosen
}

func keyOverridden() ui.Message {
	return ui.Message{
		Kind: ui.KindWarning,
		Text: keys.AppKeyEnv + " is set, and is not the app key sennit connect stored",
		Explanation: []string{"The variable is used, so this run reaches the account its key was " +
			"issued for, which may not be the one holding this vault's records."},
		Hints: []string{"remove " + keys.AppKeyEnv + " from the environment to use the stored key"},
	}
}

// secretsRow says where this run's phrase and key came from, for status.
func secretsRow(sources credentialSources, place profile.Place) string {
	from := func(source string) string {
		if source == fromProfile {
			return fromProfile + ", in " + place.Name
		}
		return source
	}
	switch {
	case sources.phrase == "":
		return ""
	case sources.key == "":
		return "phrase from " + from(sources.phrase)
	case sources.phrase == fromProfile && sources.key == fromProfile:
		return "phrase and app key from " + from(fromProfile)
	default:
		return "phrase from " + from(sources.phrase) + ", app key from " + from(sources.key)
	}
}
