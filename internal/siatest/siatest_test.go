package siatest_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/internal/siatest"
	"github.com/steven3002/sennit/sia"
)

// A client connects to the indexer and reads a ready account, and an upload
// through it is refused for want of hosts, before anything is written.
func TestAnUploadThroughTheIndexerIsRefusedForWantOfHosts(t *testing.T) {
	indexer, key := siatest.IndexerWithoutHosts(t)
	client, err := sia.Connect(sia.Config{Indexer: indexer, AppKey: key})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	account, err := client.Account(ctx)
	if err != nil {
		t.Fatalf("read the account: %v", err)
	}
	if !account.Ready {
		t.Error("the account is not ready, so a first write would wait for funding instead of failing")
	}

	_, err = client.UploadPacked(ctx, [][]byte{[]byte("a sealed record")})
	if err == nil {
		t.Fatal("an upload with no hosts to take it succeeded")
	}
	if !strings.Contains(err.Error(), "not enough hosts") {
		t.Errorf("the upload failed with %q, want the refusal a network with no hosts gives", err)
	}
}
