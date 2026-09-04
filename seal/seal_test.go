package seal_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
)

func testSealer(t *testing.T) *seal.Sealer {
	t.Helper()
	hierarchy, err := keys.Derive(keys.Seed{1, 2, 3})
	if err != nil {
		t.Fatalf("derive keys: %v", err)
	}
	sealer, err := seal.New(hierarchy.Record, hierarchy.Content)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	t.Cleanup(sealer.Close)
	return sealer
}

func TestSealRoundTripIsByteExact(t *testing.T) {
	sealer := testSealer(t)
	plaintext := []byte(strings.Repeat("the packer budgets in ciphertext bytes. ", 40))

	sealed, err := sealer.Seal(plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	opened, err := sealer.Open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatal("round trip is not byte-exact")
	}
	if bytes.Contains(sealed, plaintext[:32]) {
		t.Fatal("sealed bytes contain plaintext")
	}
}

// Two seals of one plaintext must differ, or an observer learns that two
// records are the same without decrypting either.
func TestSealIsUnlinkable(t *testing.T) {
	sealer := testSealer(t)
	plaintext := []byte("the user prefers pnpm over npm")

	first, err := sealer.Seal(plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	second, err := sealer.Seal(plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two seals of the same plaintext are identical")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	sealer := testSealer(t)
	sealed, err := sealer.Seal([]byte("a statement worth protecting"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	sealed[len(sealed)-1] ^= 0x01

	if _, err := sealer.Open(sealed); err == nil {
		t.Fatal("a flipped tag bit was accepted")
	}
}

// The content address must depend on the key, or anyone holding a candidate
// plaintext can confirm that a vault stores it.
func TestCIDIsKeyed(t *testing.T) {
	body := []byte("the grant milestone review is at the end of month two")

	first := testSealer(t)
	firstCID, err := first.CID(body)
	if err != nil {
		t.Fatalf("cid: %v", err)
	}
	sameCID, err := first.CID(body)
	if err != nil {
		t.Fatalf("cid: %v", err)
	}
	if firstCID != sameCID {
		t.Fatal("the same body under the same key produced two addresses")
	}

	other, err := keys.Derive(keys.Seed{9, 9, 9})
	if err != nil {
		t.Fatalf("derive keys: %v", err)
	}
	secondSealer, err := seal.New(other.Record, other.Content)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	defer secondSealer.Close()
	secondCID, err := secondSealer.CID(body)
	if err != nil {
		t.Fatalf("cid: %v", err)
	}
	if firstCID == secondCID {
		t.Fatal("two vaults produced the same address for one body")
	}
}

func TestFrameRoundTrip(t *testing.T) {
	id, err := record.NewID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	sealed := bytes.Repeat([]byte{0xAB}, 857)

	framed, err := seal.FrameRecord(record.KindMemory, id, sealed)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	frame, rest, err := seal.Unframe(framed)
	if err != nil {
		t.Fatalf("unframe: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("%d bytes left after a single frame", len(rest))
	}
	if frame.ID != id || frame.Kind != record.KindMemory {
		t.Fatalf("frame identifies %s/%s, want %s/%s", frame.Kind, frame.ID, record.KindMemory, id)
	}
	if !bytes.Equal(frame.Sealed, sealed) {
		t.Fatal("framing did not preserve the sealed bytes")
	}
	if got, want := len(framed)-len(sealed), seal.FrameOverhead(len(sealed)); got != want {
		t.Fatalf("framing cost %d bytes, FrameOverhead reports %d", got, want)
	}
}

// Many records share one blob, so the parser has to walk forward through them.
func TestUnframeWalksAConcatenatedBlob(t *testing.T) {
	var blob []byte
	ids := make([]record.ID, 3)
	for n := range ids {
		id, err := record.NewID()
		if err != nil {
			t.Fatalf("new id: %v", err)
		}
		ids[n] = id
		framed, err := seal.FrameRecord(record.KindMemory, id, bytes.Repeat([]byte{byte(n)}, 100+n))
		if err != nil {
			t.Fatalf("frame: %v", err)
		}
		blob = append(blob, framed...)
	}

	rest := blob
	for n := range ids {
		var frame seal.Frame
		var err error
		if frame, rest, err = seal.Unframe(rest); err != nil {
			t.Fatalf("unframe %d: %v", n, err)
		}
		if frame.ID != ids[n] {
			t.Fatalf("frame %d identifies %s, want %s", n, frame.ID, ids[n])
		}
	}
	if len(rest) != 0 {
		t.Fatalf("%d bytes left after walking every frame", len(rest))
	}
}

// A blob truncated mid-frame must report itself rather than return a partial
// record: damage has to stay bounded to the tail.
func TestUnframeRejectsTruncation(t *testing.T) {
	id, _ := record.NewID()
	framed, err := seal.FrameRecord(record.KindMemory, id, bytes.Repeat([]byte{0x11}, 200))
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if _, _, err := seal.Unframe(framed[:len(framed)-10]); err == nil {
		t.Fatal("a truncated frame was accepted")
	}
}
