package seal_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
)

// packed builds a blob of framed records the way a flush does, and returns the
// blob alongside what each record should read back as.
func packed(t *testing.T, sealer *seal.Sealer, count int) ([]byte, []record.ID, [][]byte) {
	t.Helper()

	var blob bytes.Buffer
	ids := make([]record.ID, count)
	bodies := make([][]byte, count)
	for i := range count {
		id, err := record.NewID()
		if err != nil {
			t.Fatalf("record id: %v", err)
		}
		body := fmt.Appendf(nil, "record %d: a statement worth keeping, with the context that resolves it", i)
		sealed, err := sealer.Seal(body)
		if err != nil {
			t.Fatalf("seal %d: %v", i, err)
		}
		framed, err := seal.FrameRecord(record.KindMemory, id, sealed)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		blob.Write(framed)
		ids[i], bodies[i] = id, body
	}
	return blob.Bytes(), ids, bodies
}

// Framing is what lets a packed object be taken apart without a catalog: each
// record carries its own identity next to its ciphertext.
func TestFramesRecoverEveryRecordWithNoCatalog(t *testing.T) {
	sealer := testSealer(t)
	const count = 200
	blob, ids, bodies := packed(t, sealer, count)

	frames, err := seal.Frames(blob)
	if err != nil {
		t.Fatalf("parse frames: %v", err)
	}
	if len(frames) != count {
		t.Fatalf("recovered %d frames from %d records", len(frames), count)
	}
	for i, frame := range frames {
		if frame.ID != ids[i] {
			t.Fatalf("frame %d identifies as %s, want %s", i, frame.ID, ids[i])
		}
		if frame.Kind != record.KindMemory {
			t.Fatalf("frame %d is kind %q", i, frame.Kind)
		}
		body, err := sealer.Open(frame.Sealed)
		if err != nil {
			t.Fatalf("open frame %d: %v", i, err)
		}
		if !bytes.Equal(body, bodies[i]) {
			t.Fatalf("frame %d is not byte-exact", i)
		}
	}
}

// The framing overhead is what buys the recovery path, so it has to stay
// small enough that the insurance is worth the premium.
func TestFrameOverheadStaysSmall(t *testing.T) {
	sealer := testSealer(t)
	const count = 100
	blob, _, _ := packed(t, sealer, count)

	frames, err := seal.Frames(blob)
	if err != nil {
		t.Fatalf("parse frames: %v", err)
	}
	var ciphertext int
	for _, frame := range frames {
		ciphertext += len(frame.Sealed)
	}
	overhead := len(blob) - ciphertext
	perRecord := float64(overhead) / count

	t.Logf("%d B/record framing over %d B of ciphertext, %.2f%% of the blob",
		overhead/count, ciphertext/count, 100*float64(overhead)/float64(len(blob)))
	if perRecord > 32 {
		t.Fatalf("framing costs %.1f B/record", perRecord)
	}
}

// Damage must be bounded rather than fatal. A blob that has been truncated or
// corrupted still has to yield the records before the break, or one bad byte
// costs a whole slab's worth of memories instead of one record's.
//
// Parsing alone is not a guarantee of correctness and is not treated as one.
// Every frame body starts with the same magic, so damage to a length header
// can resynchronise the reader onto a frame boundary that is not one, and the
// frame that comes out is structurally plausible and semantically wrong. The
// envelope is what settles it: a frame that decrypts is the record it says it
// is, and a frame that does not is discarded. That is the reason the vault
// applies its own authenticated encryption rather than trusting the transport.
func TestDamageIsBoundedToTheTail(t *testing.T) {
	sealer := testSealer(t)
	const count = 50
	blob, ids, bodies := packed(t, sealer, count)

	want := make(map[record.ID][]byte, count)
	for i, id := range ids {
		want[id] = bodies[i]
	}

	for _, tc := range []struct {
		name  string
		blob  []byte
		clean bool
		// least is the number of records that must survive, as a floor on how
		// far the damage is allowed to reach back from where it happened.
		least int
	}{
		{name: "intact", blob: blob, clean: true, least: count},
		{name: "truncated mid-frame", blob: blob[:len(blob)-37], least: count - 1},
		{name: "truncated mid-header", blob: blob[:len(blob)-2], least: count - 1},
		{name: "corrupt length in the first frame", blob: withByte(blob, 0, 0xff)},
		{name: "corrupt length in the last frame", blob: withByte(blob, len(blob)-145, 0xff), least: count - 1},
		{name: "first kilobyte only", blob: blob[:1024], least: 5},
		{name: "empty", blob: nil, clean: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames, err := seal.Frames(tc.blob)
			if tc.clean && err != nil {
				t.Fatalf("parse: %v", err)
			}

			var recovered, rejected int
			for _, frame := range frames {
				body, err := sealer.Open(frame.Sealed)
				if err != nil {
					rejected++
					continue
				}
				// A frame that opens must be the record it claims to be. If
				// the envelope ever let a mislabelled record through, partial
				// recovery would be worse than none.
				expected, known := want[frame.ID]
				if !known {
					t.Fatalf("a frame decrypted under this key with an id the vault never wrote: %s", frame.ID)
				}
				if !bytes.Equal(body, expected) {
					t.Fatalf("record %s opened but is not byte-exact", frame.ID)
				}
				recovered++
			}
			if recovered < tc.least {
				t.Fatalf("recovered %d of %d records, want at least %d", recovered, count, tc.least)
			}
			t.Logf("%d frames parsed, %d records byte-exact, %d rejected by the envelope: %v",
				len(frames), recovered, rejected, err)
		})
	}
}

// Corrupting one record's ciphertext must cost that record and no other. The
// envelope is what detects it; the framing is what contains it.
func TestCorruptCiphertextCostsOnlyItsOwnRecord(t *testing.T) {
	sealer := testSealer(t)
	const count = 20
	blob, _, bodies := packed(t, sealer, count)

	frames, err := seal.Frames(blob)
	if err != nil {
		t.Fatalf("parse frames: %v", err)
	}
	// Flip a bit inside the middle record's ciphertext, in place, so the frame
	// lengths are untouched and only the payload is wrong.
	target := frames[count/2]
	offset := bytes.Index(blob, target.Sealed)
	if offset < 0 {
		t.Fatal("could not locate the target ciphertext")
	}
	damaged := withByte(blob, offset+len(target.Sealed)/2, blob[offset+len(target.Sealed)/2]^0x01)

	frames, err = seal.Frames(damaged)
	if err != nil {
		t.Fatalf("parse frames after damage: %v", err)
	}
	if len(frames) != count {
		t.Fatalf("a flipped bit cost %d frames", count-len(frames))
	}
	var lost int
	for i, frame := range frames {
		body, err := sealer.Open(frame.Sealed)
		if err != nil {
			lost++
			continue
		}
		if !bytes.Equal(body, bodies[i]) {
			t.Fatalf("frame %d opened but is not byte-exact, so the envelope did not catch the damage", i)
		}
	}
	if lost != 1 {
		t.Fatalf("a flipped bit cost %d records, want exactly 1", lost)
	}
	t.Log("a flipped bit costs one record; every other record in the blob opens byte-exact")
}

func withByte(blob []byte, at int, value byte) []byte {
	out := bytes.Clone(blob)
	out[at] = value
	return out
}
