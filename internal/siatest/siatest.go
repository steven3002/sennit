// Package siatest stands in for a Sia indexer in tests.
//
// It answers the few calls a vault makes to connect and to read its account,
// and it offers no hosts. Every upload through it fails the way a write does
// when the network cannot take it, with "not enough hosts available", before a
// byte leaves the machine. That is the whole of what it is for: a test of what
// a write says when the network refuses its flush needs a network that refuses,
// and the vault's own fakes all stand in for one that accepts. Nothing here can
// bill a slab, because nothing here can store one.
package siatest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/steven3002/sennit/keys"
)

// IndexerWithoutHosts starts an indexer that authorizes any app key and has no
// hosts to upload to. It returns the indexer's address and an app key to
// connect with, and it stops when the test ends.
//
// The account it reports is ready, so a vault's first write does not wait for
// funding that will never come, and the upload is what fails.
func IndexerWithoutHosts(t testing.TB) (indexer string, key keys.AppKey) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/auth/check":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/hosts":
			answer(w, []struct{}{})
		case r.Method == http.MethodGet && r.URL.Path == "/account":
			answer(w, map[string]any{"ready": true, "maxPinnedData": uint64(1) << 40})
		default:
			// Anything else would be a write reaching further than an upload
			// with no hosts can, and the test should hear about it.
			http.Error(w, "the test indexer does not answer "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
		}
	}))
	t.Cleanup(server.Close)

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate an app key: %v", err)
	}
	return server.URL, keys.AppKey(private)
}

func answer(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}
