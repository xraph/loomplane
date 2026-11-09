package repositories

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// maxCommitMessage caps the commit message Loomplane keeps. A head commit is
// read to say what changed, and a message longer than this is a changelog,
// not a summary.
const maxCommitMessage = 4 << 10

// The kinds of Release.
const (
	// ReleaseKindRelease is a release the host publishes, with a name and
	// notes.
	ReleaseKindRelease = "release"
	// ReleaseKindTag is a bare tag: the repository has no published release,
	// or its host has no releases at all (Bitbucket).
	ReleaseKindTag = "tag"
)

// Commit is the commit a branch points at now.
type Commit struct {
	SHA string `json:"sha"`
	// Ref is the branch, tag or commit that was read: the default branch's
	// name when the caller asked for none.
	Ref string `json:"ref"`
	// Message is the commit message, cut at 4 KiB. It is untrusted text.
	Message string `json:"message"`
	// Author is the commit author's name, never their email. It is untrusted
	// text too: whoever commits picks it.
	Author string `json:"author"`
	// Date is when the commit was committed, the author's date when the host
	// doesn't say.
	Date time.Time `json:"date"`
	// URL is the commit's page on its host.
	URL string `json:"url"`
}

// Release is a repository's latest release, or its newest tag when it has
// none.
type Release struct {
	// Name is the release's title, the tag's name for a bare tag. It is
	// untrusted text.
	Name string `json:"name"`
	// Tag is the tag the release is on. It is untrusted text.
	Tag string `json:"tag"`
	// URL is the release's or the tag's page on its host.
	URL string `json:"url"`
	// PublishedAt is zero when the host doesn't say.
	PublishedAt time.Time `json:"published_at"`
	// Kind is ReleaseKindRelease or ReleaseKindTag.
	Kind string `json:"kind"`
}

// StateReader reads where a repository is now: the commit a branch points
// at and its latest release, with the addresses of their pages on the host.
// *GitHub, *GitLab and *Bitbucket are each one.
type StateReader interface {
	// HeadCommit reads the commit ref points at, or the default branch's
	// head when ref is empty.
	HeadCommit(ctx context.Context, repo Repository, ref, token string) (Commit, error)
	// LatestRelease reads the newest release, falling back to the newest
	// tag. A repository with neither is nil and no error.
	LatestRelease(ctx context.Context, repo Repository, token string) (*Release, error)
	// RepositoryURL is the repository's page on the host.
	RepositoryURL(repo Repository) string
}

var (
	_ StateReader = (*GitHub)(nil)
	_ StateReader = (*GitLab)(nil)
	_ StateReader = (*Bitbucket)(nil)
)

// The providers Repository.Provider names.
const (
	ProviderGitHub    = "github"
	ProviderGitLab    = "gitlab"
	ProviderBitbucket = "bitbucket"
)

// Provider names the kind of host r is on: GitHub, Bitbucket, or GitLab,
// which is every other host Loomplane reads.
func (r Repository) Provider() string {
	switch {
	case r.onGitHub():
		return ProviderGitHub
	case strings.EqualFold(r.Host, HostBitbucket):
		return ProviderBitbucket
	default:
		return ProviderGitLab
	}
}

// errNoState is a host whose reader can't say where a repository is.
var errNoState = fmt.Errorf("%w: Loomplane can't read commits and releases from that host", ErrUnsupportedHost)

// state returns the StateReader of host.
func (r Readers) state(ctx context.Context, host string) (StateReader, error) {
	rd, err := r.For(ctx, host)
	if err != nil {
		return nil, err
	}
	s, ok := rd.(StateReader)
	if !ok {
		return nil, errNoState
	}
	return s, nil
}

// HeadCommit reads through the repository's host.
func (r Readers) HeadCommit(ctx context.Context, repo Repository, ref, token string) (Commit, error) {
	s, err := r.state(ctx, repo.Host)
	if err != nil {
		return Commit{}, err
	}
	return s.HeadCommit(ctx, repo, ref, token)
}

// LatestRelease reads through the repository's host.
func (r Readers) LatestRelease(ctx context.Context, repo Repository, token string) (*Release, error) {
	s, err := r.state(ctx, repo.Host)
	if err != nil {
		return nil, err
	}
	return s.LatestRelease(ctx, repo, token)
}

// RepositoryURL is the repository's page on its host, as its reader knows
// it: a self-managed GitLab's own address, port and all. A host nobody reads
// gets Repository.URL, which is right for every static host.
func (r Readers) RepositoryURL(ctx context.Context, repo Repository) string {
	s, err := r.state(ctx, repo.Host)
	if err != nil {
		return repo.URL()
	}
	return s.RepositoryURL(repo)
}

// cutMessage trims a commit message and cuts it at maxCommitMessage bytes,
// never inside a character.
func cutMessage(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxCommitMessage {
		return s
	}
	return strings.ToValidUTF8(s[:maxCommitMessage], "") + "…"
}

// pageRef is a ref as one segment or more of a page's path, or "" when it
// isn't a ref Loomplane would put in a URL. A tag is the repository's own
// text, so only the plain names ValidRef allows become links.
func pageRef(ref string) string {
	if !ValidRef(ref) {
		return ""
	}
	return escapeRef(ref)
}

// GitHub.

// DefaultGitHubWebURL is github.com's web address.
const DefaultGitHubWebURL = "https://" + githubDomain

// webURL is where GitHub serves its pages.
func (g *GitHub) webURL() string {
	if g.WebURL == "" {
		return DefaultGitHubWebURL
	}
	return strings.TrimRight(g.WebURL, "/")
}

// RepositoryURL implements StateReader.
func (g *GitHub) RepositoryURL(repo Repository) string {
	return g.webURL() + "/" + repo.FullName()
}

// CommitURL is the page of commit sha.
func (g *GitHub) CommitURL(repo Repository, sha string) string {
	return g.RepositoryURL(repo) + "/commit/" + sha
}

// releaseURL is the page of the release on tag, or of the tag itself when
// kind is ReleaseKindTag. A tag that can't be a link gets the list.
func (g *GitHub) releaseURL(repo Repository, tag, kind string) string {
	ref := pageRef(tag)
	switch {
	case kind == ReleaseKindRelease && ref != "":
		return g.RepositoryURL(repo) + "/releases/tag/" + ref
	case kind == ReleaseKindRelease:
		return g.RepositoryURL(repo) + "/releases"
	case ref != "":
		return g.RepositoryURL(repo) + "/tree/" + ref
	default:
		return g.RepositoryURL(repo) + "/tags"
	}
}

// defaultBranch reads the name of the repository's default branch.
func (g *GitHub) defaultBranch(ctx context.Context, repo Repository, token string) (string, error) {
	var info struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.getJSON(ctx, "/repos/"+repo.FullName(), token, &info); err != nil {
		return "", err
	}
	if info.DefaultBranch == "" {
		return "", fmt.Errorf("%w: %s has no default branch", ErrNotFound, repo.FullName())
	}
	return info.DefaultBranch, nil
}

// githubCommit is the commit part of GitHub's commit answers.
type githubCommit struct {
	Message string `json:"message"`
	Author  struct {
		Name string    `json:"name"`
		Date time.Time `json:"date"`
	} `json:"author"`
	Committer struct {
		Date time.Time `json:"date"`
	} `json:"committer"`
}

func (c githubCommit) date() time.Time {
	if !c.Committer.Date.IsZero() {
		return c.Committer.Date.UTC()
	}
	return c.Author.Date.UTC()
}

// HeadCommit implements StateReader. It lists one commit from ref rather
// than reading the commit itself, whose answer carries every changed file
// and can run to megabytes.
func (g *GitHub) HeadCommit(ctx context.Context, repo Repository, ref, token string) (Commit, error) {
	if ref == "" {
		branch, err := g.defaultBranch(ctx, repo, token)
		if err != nil {
			return Commit{}, err
		}
		ref = branch
	}
	if !ValidRef(ref) {
		return Commit{}, fmt.Errorf("%w: %q isn't a valid ref", ErrNotFound, ref)
	}
	var list []struct {
		SHA    string       `json:"sha"`
		Commit githubCommit `json:"commit"`
	}
	if err := g.getJSON(ctx, "/repos/"+repo.FullName()+"/commits?per_page=1&sha="+url.QueryEscape(ref), token, &list); err != nil {
		return Commit{}, err
	}
	if len(list) == 0 {
		return Commit{}, fmt.Errorf("%w: %q has no commits", ErrNotFound, ref)
	}
	c := list[0]
	if !shaPattern.MatchString(c.SHA) {
		return Commit{}, fmt.Errorf("GitHub answered %q for a commit SHA", c.SHA)
	}
	return Commit{
		SHA: c.SHA, Ref: ref, Message: cutMessage(c.Commit.Message), Author: c.Commit.Author.Name,
		Date: c.Commit.date(), URL: g.CommitURL(repo, c.SHA),
	}, nil
}

// LatestRelease implements StateReader: GitHub's latest release, which is
// the newest one that is neither a draft nor a prerelease, else the first
// tag GitHub lists, dated by its commit when that can be read.
func (g *GitHub) LatestRelease(ctx context.Context, repo Repository, token string) (*Release, error) {
	var rel struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		PublishedAt time.Time `json:"published_at"`
	}
	err := g.getJSON(ctx, "/repos/"+repo.FullName()+"/releases/latest", token, &rel)
	switch {
	case err == nil && rel.TagName != "":
		name := rel.Name
		if name == "" {
			name = rel.TagName
		}
		return &Release{
			Name: name, Tag: rel.TagName, PublishedAt: rel.PublishedAt.UTC(), Kind: ReleaseKindRelease,
			URL: g.releaseURL(repo, rel.TagName, ReleaseKindRelease),
		}, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return nil, err
	}
	// No release, which GitHub answers with a 404. A repository Loomplane
	// can't read answers 404 too, and the tags then say so.
	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := g.getJSON(ctx, "/repos/"+repo.FullName()+"/tags?per_page=1", token, &tags); err != nil {
		return nil, err
	}
	if len(tags) == 0 || tags[0].Name == "" {
		return nil, nil
	}
	t := tags[0]
	out := &Release{Name: t.Name, Tag: t.Name, Kind: ReleaseKindTag, URL: g.releaseURL(repo, t.Name, ReleaseKindTag)}
	// The Git data API answers with the commit alone, no files. Without a
	// date the tag still stands.
	if shaPattern.MatchString(t.Commit.SHA) {
		var c struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		}
		if g.getJSON(ctx, "/repos/"+repo.FullName()+"/git/commits/"+t.Commit.SHA, token, &c) == nil {
			out.PublishedAt = c.Committer.Date.UTC()
		}
	}
	return out, nil
}

// GitLab.

// RepositoryURL implements StateReader: the project's page on its own
// instance, which for a self-managed one keeps its port.
func (g *GitLab) RepositoryURL(repo Repository) string {
	return g.BaseURL + "/" + repo.FullName()
}

// CommitURL is the page of commit sha.
func (g *GitLab) CommitURL(repo Repository, sha string) string {
	return g.RepositoryURL(repo) + "/-/commit/" + sha
}

// releaseURL is the page of the release on tag, or of the tag itself when
// kind is ReleaseKindTag. A tag that can't be a link gets the list. GitLab
// takes the tag as one escaped segment.
func (g *GitLab) releaseURL(repo Repository, tag, kind string) string {
	page := "/-/releases"
	if kind == ReleaseKindTag {
		page = "/-/tags"
	}
	if pageRef(tag) == "" {
		return g.RepositoryURL(repo) + page
	}
	return g.RepositoryURL(repo) + page + "/" + url.PathEscape(tag)
}

// HeadCommit implements StateReader.
func (g *GitLab) HeadCommit(ctx context.Context, repo Repository, ref, token string) (Commit, error) {
	if ref == "" {
		p, err := g.Project(ctx, repo, token)
		if err != nil {
			return Commit{}, err
		}
		if p.DefaultBranch == "" {
			return Commit{}, fmt.Errorf("%w: %s has no default branch", ErrNotFound, repo.FullName())
		}
		ref = p.DefaultBranch
	}
	if !ValidRef(ref) {
		return Commit{}, fmt.Errorf("%w: %q isn't a valid ref", ErrNotFound, ref)
	}
	var c struct {
		ID            string    `json:"id"`
		Message       string    `json:"message"`
		AuthorName    string    `json:"author_name"`
		AuthoredDate  time.Time `json:"authored_date"`
		CommittedDate time.Time `json:"committed_date"`
	}
	if err := g.getJSON(ctx, projectPath(repo)+"/repository/commits/"+url.PathEscape(ref), token, &c); err != nil {
		return Commit{}, err
	}
	if !shaPattern.MatchString(c.ID) {
		return Commit{}, fmt.Errorf("GitLab answered %q for a commit SHA", c.ID)
	}
	date := c.CommittedDate
	if date.IsZero() {
		date = c.AuthoredDate
	}
	return Commit{
		SHA: c.ID, Ref: ref, Message: cutMessage(c.Message), Author: c.AuthorName,
		Date: date.UTC(), URL: g.CommitURL(repo, c.ID),
	}, nil
}

// LatestRelease implements StateReader: the most recently released
// release, else the most recently updated tag. A project whose releases
// are turned off answers 403, which is read as none.
func (g *GitLab) LatestRelease(ctx context.Context, repo Repository, token string) (*Release, error) {
	var rels []struct {
		Name       string    `json:"name"`
		TagName    string    `json:"tag_name"`
		ReleasedAt time.Time `json:"released_at"`
	}
	err := g.getJSON(ctx, projectPath(repo)+"/releases?order_by=released_at&sort=desc&per_page=1", token, &rels)
	switch {
	case err == nil && len(rels) > 0 && rels[0].TagName != "":
		r := rels[0]
		name := r.Name
		if name == "" {
			name = r.TagName
		}
		return &Release{
			Name: name, Tag: r.TagName, PublishedAt: r.ReleasedAt.UTC(), Kind: ReleaseKindRelease,
			URL: g.releaseURL(repo, r.TagName, ReleaseKindRelease),
		}, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return nil, err
	}
	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			CommittedDate time.Time `json:"committed_date"`
		} `json:"commit"`
	}
	if err := g.getJSON(ctx, projectPath(repo)+"/repository/tags?order_by=updated&sort=desc&per_page=1", token, &tags); err != nil {
		return nil, err
	}
	if len(tags) == 0 || tags[0].Name == "" {
		return nil, nil
	}
	t := tags[0]
	return &Release{
		Name: t.Name, Tag: t.Name, PublishedAt: t.Commit.CommittedDate.UTC(), Kind: ReleaseKindTag,
		URL: g.releaseURL(repo, t.Name, ReleaseKindTag),
	}, nil
}

// Bitbucket.

// RepositoryURL implements StateReader.
func (b *Bitbucket) RepositoryURL(repo Repository) string {
	return b.WebURL + "/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name)
}

// CommitURL is the page of commit sha.
func (b *Bitbucket) CommitURL(repo Repository, sha string) string {
	return b.RepositoryURL(repo) + "/commits/" + sha
}

// tagURL is the source at tag, Bitbucket's nearest thing to a tag's page.
// A tag that can't be a link gets the repository's branches and tags.
func (b *Bitbucket) tagURL(repo Repository, tag string) string {
	if ref := pageRef(tag); ref != "" {
		return b.RepositoryURL(repo) + "/src/" + ref
	}
	return b.RepositoryURL(repo) + "/branches"
}

// HeadCommit implements StateReader. It resolves ref the way Resolve does,
// a branch before a tag before a commit, then reads that commit.
func (b *Bitbucket) HeadCommit(ctx context.Context, repo Repository, ref, token string) (Commit, error) {
	if ref == "" {
		name, err := b.DefaultBranch(ctx, repo, token)
		if err != nil {
			return Commit{}, err
		}
		ref = name
	}
	sha, err := b.Resolve(ctx, repo, ref, token)
	if err != nil {
		return Commit{}, err
	}
	var c struct {
		Hash    string    `json:"hash"`
		Message string    `json:"message"`
		Date    time.Time `json:"date"`
		Author  struct {
			Raw  string `json:"raw"`
			User struct {
				DisplayName string `json:"display_name"`
			} `json:"user"`
		} `json:"author"`
	}
	if err := b.getJSON(ctx, b.repoAPI(repo)+"/commit/"+sha, token, &c); err != nil {
		return Commit{}, normalize(err)
	}
	if !shaPattern.MatchString(c.Hash) {
		return Commit{}, fmt.Errorf("Bitbucket answered %q for a commit hash", c.Hash)
	}
	author := c.Author.User.DisplayName
	if author == "" {
		author = rawAuthorName(c.Author.Raw)
	}
	return Commit{
		SHA: c.Hash, Ref: ref, Message: cutMessage(c.Message), Author: author,
		Date: c.Date.UTC(), URL: b.CommitURL(repo, c.Hash),
	}, nil
}

// rawAuthorName is the name in Git's "Name <email>", without the email.
func rawAuthorName(raw string) string {
	name, _, _ := strings.Cut(raw, "<")
	return strings.TrimSpace(name)
}

// LatestRelease implements StateReader. Bitbucket has no releases, so it's
// always the tag on the newest commit.
func (b *Bitbucket) LatestRelease(ctx context.Context, repo Repository, token string) (*Release, error) {
	var page struct {
		Values []struct {
			Name   string `json:"name"`
			Target struct {
				Date time.Time `json:"date"`
			} `json:"target"`
		} `json:"values"`
	}
	if err := b.getJSON(ctx, b.repoAPI(repo)+"/refs/tags?sort=-target.date&pagelen=1", token, &page); err != nil {
		return nil, normalize(err)
	}
	if len(page.Values) == 0 || page.Values[0].Name == "" {
		return nil, nil
	}
	t := page.Values[0]
	return &Release{
		Name: t.Name, Tag: t.Name, PublishedAt: t.Target.Date.UTC(), Kind: ReleaseKindTag,
		URL: b.tagURL(repo, t.Name),
	}, nil
}
