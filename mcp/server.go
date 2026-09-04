// Package mcp exposes the vault over the Model Context Protocol.
//
// It is a skin over vault and holds no logic the command line would also want.
// Anything a second interface would need belongs one layer down; what lives here
// is the protocol surface itself, an address space, six tools, the resources
// they link to, and one prompt.
package mcp

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/vault"
)

// Name and Version identify this server to a client.
const (
	Name    = "sennit"
	Title   = "Sennit"
	Version = "0.1.0"
)

// ErrNotOnboarded reports that this process has no usable vault.
//
// It is returned from tools rather than refused at connect. A server that
// declines the connection tells the model nothing; one that answers every call
// with what to do about it gives the user a sentence they can act on, which is
// what the specification asks for.
var ErrNotOnboarded = errors.New("this Sennit server has no vault open")

// A Server exposes one vault to a protocol client.
//
// Each client launches its own process against the same vault, which is why the
// device store is multi-process safe from the outset rather than as a later
// hardening pass. Two clients running at once is the ordinary case, not the
// exceptional one.
type Server struct {
	vault *vault.Vault
	// openErr is why there is no vault, when there is none. It is carried
	// rather than returned at construction so the guide and the protocol
	// surface still work, and every tool can say what is wrong.
	openErr error
	sdk     *sdk.Server
}

// New builds a server over a vault.
func New(v *vault.Vault) *Server { return build(&Server{vault: v}) }

// Unopened builds a server that has no vault, and says why to anything that
// asks it to do something.
func Unopened(err error) *Server {
	return build(&Server{openErr: fmt.Errorf("%w: %w", ErrNotOnboarded, err)})
}

// ready reports whether there is a vault to work with.
func (s *Server) ready() error {
	if s.vault == nil {
		if s.openErr != nil {
			return s.openErr
		}
		return ErrNotOnboarded
	}
	return nil
}

// Run serves one client over a transport until the context ends.
func (s *Server) Run(ctx context.Context, transport sdk.Transport) error {
	return s.sdk.Run(ctx, transport)
}

// Connect attaches the server to a transport without blocking, for a caller
// that drives the session itself.
func (s *Server) Connect(ctx context.Context, transport sdk.Transport) (*sdk.ServerSession, error) {
	return s.sdk.Connect(ctx, transport, nil)
}

// PageSize bounds every protocol list. The tool surface pages in band, because
// tools/call has no protocol pagination at all.
const PageSize = 100

func build(s *Server) *Server {
	s.sdk = sdk.NewServer(
		&sdk.Implementation{
			Name:    Name,
			Title:   Title,
			Version: Version,
			Description: "The user's own encrypted store of memories and past conversations, " +
				"held on the Sia network.",
		},
		&sdk.ServerOptions{
			Instructions: Instructions,
			PageSize:     PageSize,
			// Stated rather than inherited. The SDK's default advertises
			// logging, which is deprecated and which this server does not
			// implement, and a capability advertised without a handler is a
			// claim a client will act on and then find false. Subscriptions are
			// absent for the same reason: nothing here serves them yet, so
			// nothing here announces them.
			Capabilities: &sdk.ServerCapabilities{
				Tools:     &sdk.ToolCapabilities{},
				Resources: &sdk.ResourceCapabilities{},
				Prompts:   &sdk.PromptCapabilities{},
			},
		})
	// A user's own records must not be cached by anything but the client that
	// asked for them.
	s.sdk.AddReceivingMiddleware(privateCache)

	s.registerTools()
	s.registerResources()
	s.registerPrompts()
	return s
}

// privateCache marks every result as private to the requesting client.
//
// The SDK sets `cacheScope: "public"` on every list and every read, and it does
// so *after* the handler returns, so a handler cannot override it and this is
// the only place that can. Observed on the wire, and it matters here more than
// in most servers: the listings and reads this server answers are one user's own
// memories, and "public" invites any intermediary to keep a copy.
func privateCache(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		result, err := next(ctx, method, req)
		if err != nil {
			return result, err
		}
		switch typed := result.(type) {
		case *sdk.ReadResourceResult:
			typed.CacheScope = cacheScopePrivate
		case *sdk.ListResourcesResult:
			typed.CacheScope = cacheScopePrivate
		case *sdk.ListResourceTemplatesResult:
			typed.CacheScope = cacheScopePrivate
		case *sdk.ListPromptsResult:
			typed.CacheScope = cacheScopePrivate
		case *sdk.ListToolsResult:
			typed.CacheScope = cacheScopePrivate
		case *sdk.DiscoverResult:
			typed.CacheScope = cacheScopePrivate
		}
		return result, err
	}
}

const cacheScopePrivate = "private"

// registerResources publishes the address space.
//
// Every handler here is one line long on purpose: it parses and calls Resolve,
// which is the same function the `open` tool calls. That is what makes the two
// entry points structurally unable to disagree, rather than merely expected not
// to.
func (s *Server) registerResources() {
	s.sdk.AddResource(&sdk.Resource{
		URI:      VaultURI,
		Name:     "vault",
		Title:    "This vault",
		MIMEType: "application/json",
		Description: "What this vault holds, what it still owes the network, and the tags it " +
			"already uses. Read it before a first write: reusing an existing tag rather than " +
			"coining a synonym is what keeps related records findable together.",
	}, s.readResource)

	s.sdk.AddResource(&sdk.Resource{
		URI:         GuideURI,
		Name:        "guide",
		Title:       "How to use this vault",
		MIMEType:    "text/markdown",
		Description: "How to write a memory that can be found again, and how search here behaves.",
	}, s.readResource)

	s.sdk.AddResourceTemplate(&sdk.ResourceTemplate{
		URITemplate: MemoryTemplate,
		Name:        "memory",
		Title:       "A stored memory",
		MIMEType:    "text/markdown",
		Description: "One durable proposition with the context that makes it resolvable. Find an " +
			"address with recall or browse; memories are unbounded and are never enumerated.",
	}, s.readResource)

	s.sdk.AddResourceTemplate(&sdk.ResourceTemplate{
		URITemplate: SessionTemplate,
		Name:        "session",
		Title:       "A stored conversation",
		MIMEType:    "application/json",
		Description: "A conversation's title, summary, counts and links, without its transcript.",
	}, s.readResource)

	s.sdk.AddResourceTemplate(&sdk.ResourceTemplate{
		URITemplate: TranscriptTemplate,
		Name:        "transcript",
		Title:       "A conversation's turns",
		MIMEType:    "application/json",
		Description: "The messages of one conversation, in order, with tool calls still correlated " +
			"to their results. Addressed apart from the head because it is orders of magnitude larger.",
	}, s.readResource)
}

// readResource serves every resources/read, through the one resolver.
func (s *Server) readResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	resolved, err := s.Resolve(ctx, req.Params.URI, ResolveOptions{})
	if err != nil {
		if errors.Is(err, ErrNoRecord) {
			return nil, sdk.ResourceNotFoundError(req.Params.URI)
		}
		return nil, err
	}
	return &sdk.ReadResourceResult{
		Contents: []*sdk.ResourceContents{{
			URI:      resolved.Address.URI,
			MIMEType: resolved.MIMEType,
			Text:     resolved.Body,
		}},
	}, nil
}
