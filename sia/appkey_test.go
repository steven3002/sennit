package sia

import (
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/steven3002/sennit/keys"
)

// Connect converts the app key to types.PrivateKey, a byte slice whose size the
// compiler cannot enforce and whose methods panic rather than fail when it is
// wrong. The check has to be here as well as where the key is read, because
// this is the last statement before that conversion and Connect is reachable by
// any caller, not only the one that reads the environment.
//
// A panic would fail this test rather than being reported as an error, which is
// the point: the failure being guarded against is not an error value at all.
func TestConnectRefusesAKeyThatWouldPanicWhenItSigns(t *testing.T) {
	for _, n := range []int{1, 4, 31, 32, 63, 65} {
		client, err := Connect(Config{Indexer: "http://127.0.0.1:9", AppKey: make(keys.AppKey, n)})
		if err == nil {
			t.Errorf("a %d-byte app key was accepted", n)
			if client != nil {
				client.Close()
			}
			continue
		}
		if !keys.WrongAppKeyLength(err) {
			t.Errorf("a %d-byte app key gave %v, not a length failure", n, err)
		}
	}

	if _, err := Connect(Config{Indexer: "http://127.0.0.1:9"}); !keys.MissingAppKey(err) {
		t.Errorf("an absent app key gave %v, not the absent-key case", err)
	}
}

// The refusal happens before anything is dialled, so a malformed key fails the
// same way with or without a network. The address here is one nothing listens
// on: reaching it at all would be the bug.
func TestAMalformedKeyIsRefusedWithoutTouchingTheNetwork(t *testing.T) {
	_, err := Connect(Config{Indexer: "http://127.0.0.1:9", AppKey: make(keys.AppKey, ed25519.PrivateKeySize-1)})
	if err == nil {
		t.Fatal("a short app key was accepted")
	}
	var reached *IndexerError
	if errors.As(err, &reached) || errors.Is(err, ErrIndexerUnreachable) {
		t.Errorf("the key was carried to the indexer before being checked: %v", err)
	}
}
