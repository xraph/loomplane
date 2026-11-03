package repositories

import (
	"context"
	"errors"
	"time"
)

// Errors from GitLab hosts and connections.
var (
	// ErrGitLabHostExists means the organization already registered the host.
	ErrGitLabHostExists = errors.New("that GitLab host is already registered")
	// ErrGitLabHostInUse means connections still use the host.
	ErrGitLabHostInUse = errors.New("GitLab accounts are still connected through that host; disconnect them first")
	// ErrGitLabHostNotFound means the organization has no such host.
	ErrGitLabHostNotFound = errors.New("that GitLab host isn't registered")
	// ErrGitLabConnectionNotFound means there is no such GitLab connection.
	ErrGitLabConnectionNotFound = errors.New("that GitLab connection doesn't exist")
)

// Kinds of GitLab connection.
const (
	// GitLabKindOAuth is a person's GitLab account, signed in with OAuth.
	GitLabKindOAuth = "oauth"
)

// GitLabHost is a GitLab instance Loomplane can read. An instance host comes
// from the operator's settings and is nobody's to delete. The others are one
// organization's, registered through the API.
type GitLabHost struct {
	// ID is "" for an instance host, and a typeid ("glh_...") otherwise.
	ID    string
	OrgID string
	// WebURL is the instance's address with no path or trailing slash:
	// https://gitlab.com for gitlab.com.
	WebURL string
	// Host is WebURL's host, lowercased, with its port if it has one.
	Host string
	// ClientID is the OAuth application Loomplane signs in with. Its
	// secret is kept in the vault, never here.
	ClientID  string
	Instance  bool
	CreatedBy string
	CreatedAt time.Time
}

// GitLabSignIn is an OAuth sign-in that has started and not finished. The
// verifier is the PKCE code verifier, good for this one sign-in.
type GitLabSignIn struct {
	State     string
	OrgID     string
	UserID    string
	Host      string
	Verifier  string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// GitLabConnection is a GitLab account an organization has connected. The
// tokens are in the vault, not here.
type GitLabConnection struct {
	// ID is a typeid with the prefix "glc". SaveConnection makes one when
	// it is empty.
	ID    string
	OrgID string
	// Host is the GitLab host's name, as Repository.Host spells it.
	Host string
	// HostID is the registered host the connection signs in through, and ""
	// for gitlab.com and the operator's hosts.
	HostID       string
	Kind         string
	GitLabUserID int64
	Username     string
	AvatarURL    string
	// Version is the GitLab release seen at sign-in, such as "17.4.2-ee",
	// and "" when GitLab didn't say.
	Version   string
	CreatedBy string
	CreatedAt time.Time
	// LapsedAt is when GitLab stopped accepting the connection's refresh
	// token, and nil while it works.
	LapsedAt   *time.Time
	WebhookSet bool
}

// GitLabStore keeps GitLab hosts, sign-ins and connections.
type GitLabStore interface {
	// SaveHost registers a host and sets h.ID when it is empty. A host the
	// organization already has is ErrGitLabHostExists.
	SaveHost(ctx context.Context, h *GitLabHost) error
	ListHosts(ctx context.Context, orgID string) ([]*GitLabHost, error)
	// HostsNamed returns every organization's registration of host, for
	// finding a reader.
	HostsNamed(ctx context.Context, host string) ([]*GitLabHost, error)
	// DeleteHost is ErrGitLabHostInUse while connections use the host, and
	// ErrGitLabHostNotFound when the organization has no such host.
	DeleteHost(ctx context.Context, orgID, id string) error

	SaveSignIn(ctx context.Context, s *GitLabSignIn) error
	// TakeSignIn spends a sign-in. One that is missing, already spent or
	// expired at now is ErrBadState.
	TakeSignIn(ctx context.Context, state string, now time.Time) (*GitLabSignIn, error)

	// SaveConnection adds a connection, or refreshes the one the same
	// GitLab account already has with the organization on that host. It
	// returns the ID it kept and clears LapsedAt. A refresh keeps
	// WebhookSet, and a new connection's is false.
	SaveConnection(ctx context.Context, c *GitLabConnection) (string, error)
	ListConnections(ctx context.Context, orgID string) ([]*GitLabConnection, error)
	// GetConnection reads a connection whatever its organization, for the
	// webhook. A missing one is ErrGitLabConnectionNotFound.
	GetConnection(ctx context.Context, id string) (*GitLabConnection, error)
	MarkLapsed(ctx context.Context, orgID, id string, at time.Time) error
	// SetWebhookSet records that the organization's connection has a
	// webhook secret. A connection the organization doesn't have is
	// ErrGitLabConnectionNotFound.
	SetWebhookSet(ctx context.Context, orgID, id string) error
	DeleteConnection(ctx context.Context, orgID, id string) error

	// WithGrantLock runs fn holding connection id's grant lock, across
	// every Loomplane process, so two refreshes never spend one refresh
	// token.
	WithGrantLock(ctx context.Context, orgID, id string, fn func() error) error
}
