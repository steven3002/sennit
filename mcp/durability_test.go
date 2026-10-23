package mcp

import (
	"errors"
	"strings"
	"testing"
)

// The durability line has a sentence for each place a write can leave a memory,
// and only the two where the memory reached the network may say so.
//
// A flush the write set off and that failed is named with its error in both of
// the places it can leave the memory, and both say not to write the memory
// again. Through the protocol only the first can be reached in a test, because
// the second needs a network that takes the upload: it is a flush whose upload
// landed and whose record of where it landed then failed on this device.
func TestTheDurabilityLineSaysWhereTheMemoryIsAndWhatFailed(t *testing.T) {
	refused := errors.New("upload batch of 1: finalize packed upload: " +
		"not enough hosts available to upload slab 0: 0 < 30")
	uncatalogued := errors.New("catalog 4083dbdd9e9f0f816c797080eed61fba: no space left on device")

	for _, c := range []struct {
		name        string
		onNetwork   bool
		flushFailed error
		want, never []string
	}{
		{
			name:  "queued on the ordinary schedule",
			want:  []string{"Stored on this device.", "Do not tell the user it is on the network yet."},
			never: []string{"written to the network", "failed"},
		},
		{
			name:      "written to the network",
			onNetwork: true,
			want:      []string{"Stored on this device and written to the network."},
			never:     []string{"failed", "queued"},
		},
		{
			name:        "still queued after its upload failed",
			flushFailed: refused,
			want: []string{"Stored on this device, and still queued for the network", refused.Error(),
				"A later flush retries it", "do not remember it again", "do not tell the user it is on the network yet"},
			never: []string{"written to the network"},
		},
		{
			name:        "written before its flush failed",
			onNetwork:   true,
			flushFailed: uncatalogued,
			want: []string{"Stored on this device and written to the network.", uncatalogued.Error(),
				"do not remember it again"},
			never: []string{"queued", "retries"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			said := durability(c.onNetwork, c.flushFailed)
			for _, want := range c.want {
				if !strings.Contains(said, want) {
					t.Errorf("it does not say %q: %s", want, said)
				}
			}
			for _, never := range c.never {
				if strings.Contains(said, never) {
					t.Errorf("it says %q: %s", never, said)
				}
			}
		})
	}
}
