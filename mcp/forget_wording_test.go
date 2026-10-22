package mcp

import (
	"strings"
	"testing"

	"github.com/steven3002/sennit/vault"
)

// What a forget says about Sia in the two states a test's offline vault cannot
// reach through the protocol: a server that can reach Sia, which deletes the
// copy itself, and a copy already deleted.
func TestTheForgetNoteSaysWhoDeletesTheCopyOnSiaAndWhenItIsGone(t *testing.T) {
	for _, c := range []struct {
		name        string
		removal     vault.Removal
		online      bool
		words       onSiaWords
		want, never []string
	}{
		{
			name: "a memory on Sia, on a server that can reach it", removal: vault.Removal{OnNetwork: 1}, online: true,
			words: memoryOnSia,
			want: []string{
				"Its copy on Sia is not deleted yet: this server deletes it with its background flush",
				"`sennit flush` deletes it at once",
				"a `sennit hydrate` or `sennit recover` on another device can still restore it",
				"Another device that already holds the memory keeps it",
			},
			never: []string{"no connection"},
		},
		{
			name: "a transcript partly deleted", removal: vault.Removal{OnNetwork: 1, Deleted: 3}, online: true,
			words: transcriptOnSia,
			want:  []string{"Its transcript on Sia is not deleted yet", "rebuild the conversation from it"},
			never: []string{"deleted as well"},
		},
		{
			name: "a transcript deleted", removal: vault.Removal{Deleted: 4}, online: true, words: transcriptOnSia,
			want: []string{"Its transcript on Sia is deleted as well.",
				"Another device that already holds the conversation keeps it"},
			never: []string{"Until then", "not deleted yet"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			said := onSia(c.removal, c.online, c.words)
			for _, phrase := range c.want {
				if !strings.Contains(said, phrase) {
					t.Errorf("does not say %q: %q", phrase, said)
				}
			}
			for _, phrase := range c.never {
				if strings.Contains(said, phrase) {
					t.Errorf("says %q: %q", phrase, said)
				}
			}
		})
	}
}
