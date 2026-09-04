package sia

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.sia.tech/siastorage"

	"github.com/steven3002/sennit/keys"
)

// ApprovalBudget is how long an onboarding run keeps a live approval URL
// available by default.
//
// The indexer expires a connection request after about ten minutes, which is
// shorter than the time a person takes to find the right browser tab. The budget
// is the human deadline; the expiry is the indexer's, and the gap between them is
// bridged by reissuing rather than by failing.
const ApprovalBudget = 30 * time.Minute

// ErrRejected reports that the user declined the connection at the indexer.
var ErrRejected = errors.New("the connection was declined at the indexer")

// An ApprovalRequest parameterises an onboarding round.
type ApprovalRequest struct {
	// Indexer is the indexer URL; empty means DefaultIndexer.
	Indexer string
	// Budget is how long to keep a request available. Zero means
	// ApprovalBudget.
	Budget time.Duration
	// OnURL is called with each approval URL as it is issued, including every
	// reissue. A caller that shows only the first one will show an expired link
	// for most of the budget.
	OnURL func(url string, attempt int)
}

// An ApprovalResult is what an onboarding round produced.
type ApprovalResult struct {
	// AppKey authorizes this installation from now on. It is the output of the
	// approval and cannot be derived from the recovery phrase: the indexer holds
	// the other half.
	AppKey keys.AppKey
	// Attempts is how many requests were issued before one was approved. More
	// than one means the first expired while the user was still getting to it,
	// which is the ordinary case and not a failure.
	Attempts int
	// RequestFor is how long issuing a request took, and WaitedFor how long the
	// user took to approve one.
	RequestFor, WaitedFor time.Duration
}

// Approve runs the interactive approval round and returns the app key it issues.
//
// This is the step a seed phrase cannot replace, and the reason "restore from
// your recovery phrase" is not the whole story. The app key is derived from the
// phrase together with a secret the indexer issues when a human approves the
// connection, so a new device needs the phrase *and* one approval. Saying so is
// not a caveat to be buried: an onboarding flow that implies otherwise strands
// the user at exactly the moment they are trusting the product with the only
// copy of their memory.
//
// The phrase is passed straight through to the SDK's registration and is not
// retained, logged, or included in any error this function produces.
func Approve(ctx context.Context, phrase string, req ApprovalRequest) (ApprovalResult, error) {
	indexer := req.Indexer
	if indexer == "" {
		indexer = DefaultIndexer
	}
	budget := req.Budget
	if budget <= 0 {
		budget = ApprovalBudget
	}
	if phrase == "" {
		return ApprovalResult{}, keys.ErrNoPhrase
	}

	var result ApprovalResult
	deadline := time.Now().Add(budget)

	for {
		result.Attempts++
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return result, fmt.Errorf(
				"no approval within %s: %d request(s) were issued at %s and each expired unapproved",
				budget, result.Attempts-1, indexer)
		}

		// A fresh builder per attempt. Identity is carried by the app id, which
		// is a build-time constant, so reissuing does not change who is asking.
		builder := siastorage.NewBuilder(indexer, metadata())

		issued := time.Now()
		url, err := builder.RequestConnection(ctx)
		if err != nil {
			return result, fmt.Errorf("request a connection to %s: %w", indexer, err)
		}
		result.RequestFor = time.Since(issued)
		if req.OnURL != nil {
			req.OnURL(url, result.Attempts)
		}

		waitCtx, cancel := context.WithTimeout(ctx, remaining)
		waited := time.Now()
		err = builder.WaitForApproval(waitCtx)
		cancel()
		result.WaitedFor += time.Since(waited)

		switch {
		case errors.Is(err, siastorage.ErrUserRejected):
			return result, ErrRejected
		case errors.Is(err, siastorage.ErrRequestExpired):
			// The indexer's ten minutes ran out before the human did. Issue
			// another and keep a live URL in front of them.
			continue
		case err != nil:
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, fmt.Errorf("wait for approval at %s: %w", indexer, err)
		}

		sdk, err := builder.Register(ctx, phrase)
		if err != nil {
			// The phrase is deliberately absent from this message. It is the one
			// secret whose disclosure loses the whole vault, and a registration
			// failure is exactly the moment a caller is most likely to paste an
			// error somewhere public.
			return result, fmt.Errorf("register this installation with %s: %w", indexer, err)
		}
		result.AppKey = keys.AppKey(sdk.AppKey())
		return result, sdk.Close()
	}
}
