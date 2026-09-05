// Package blog reads recent posts from the Oasis blog for the digest
// automation.
//
// The site publishes no RSS or Atom feed, and its blog index is ordered
// alphabetically rather than by date, so "most recent" cannot be taken from the
// listing. Two things it does publish are used instead:
//
//   - sitemap.xml, which carries a lastmod timestamp for every post. One
//     request selects candidates without fetching a hundred pages.
//   - schema.org JSON-LD on each post, giving the headline, description and an
//     authoritative datePublished.
//
// So the reader fetches the sitemap once, takes the most recently modified
// candidates, and reads only those pages. lastmod is used for selection only;
// the ordering that matters comes from datePublished on the page itself, since
// an edit to an old post would otherwise float it to the top.
package blog

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// requestTimeout bounds a single HTTP request.
	requestTimeout = 15 * time.Second
	// maxSitemapBytes bounds the sitemap read.
	maxSitemapBytes = 4 << 20
	// maxPageBytes bounds a single post page read.
	maxPageBytes = 2 << 20
	// candidateMultiplier over-fetches candidates so that a recently edited
	// old post cannot displace a genuinely new one.
	candidateMultiplier = 3
	// maxCandidates caps that over-fetch regardless of the requested limit.
	maxCandidates = 15
	// fetchConcurrency bounds parallel page fetches, to stay a polite client.
	fetchConcurrency = 4
)

// Reader fetches recent posts from a site.
type Reader struct {
	client  *http.Client
	baseURL *url.URL
	prefix  string
}

// New returns a Reader for a blog index URL, for example
// https://www.oasis.security/blog.
//
// The URL is operator configuration, not user input, but it is still required
// to be https: the digest runs unattended, and an http endpoint would let
// anything on the path choose what tickets get filed.
func New(blogURL string, opts ...Option) (*Reader, error) {
	parsed, err := url.Parse(strings.TrimSpace(blogURL))
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("blog: %q is not a valid URL", blogURL)
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("blog: feed URL must use https, got %q", parsed.Scheme)
	}

	r := &Reader{
		client:  &http.Client{Timeout: requestTimeout},
		baseURL: parsed,
		prefix:  strings.TrimRight(parsed.Path, "/") + "/",
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// Option customises a Reader.
type Option func(*Reader)

// WithHTTPClient overrides the HTTP client, used by tests to point at a stub.
func WithHTTPClient(c *http.Client) Option {
	return func(r *Reader) { r.client = c }
}

// Recent returns up to limit posts, most recently published first.
// Post is one entry from the blog feed.
//
// It is declared here rather than in the package that consumes it, because it
// is the blog's data. The other way round -- an HTML scraper importing the
// package that owns the Temporal workflows -- made this file depend on the
// durable-execution engine and the database driver to describe four strings.
type Post struct {
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	PublishedAt time.Time `json:"publishedAt"`
	Body        string    `json:"body,omitempty"`
}

// Recent returns the newest posts from the feed, most recent first.
//
// The limit is the caller's to decide. Inventing one here would put a second
// default in a second package, and the caller that has one -- the digest --
// already names it and clamps to it; a reader comparing the two numbers would
// have no way to tell which applies.
func (r *Reader) Recent(ctx context.Context, limit int) ([]Post, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("a post limit of %d is not a number of posts to read", limit)
	}

	candidates, err := r.candidates(ctx, limit)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	posts := r.fetchAll(ctx, candidates)
	// Newest first.
	slices.SortFunc(posts, func(a, b Post) int { return b.PublishedAt.Compare(a.PublishedAt) })
	if len(posts) > limit {
		posts = posts[:limit]
	}
	return posts, nil
}

// sitemapEntry is one <url> element.
type sitemapEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

// candidates reads the sitemap and returns the most recently modified post URLs.
func (r *Reader) candidates(ctx context.Context, limit int) ([]string, error) {
	sitemapURL := *r.baseURL
	sitemapURL.Path = "/sitemap.xml"
	sitemapURL.RawQuery = ""

	body, err := r.get(ctx, sitemapURL.String(), maxSitemapBytes)
	if err != nil {
		return nil, err
	}

	var doc struct {
		URLs []sitemapEntry `xml:"url"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("blog: could not parse sitemap: %w", err)
	}

	type candidate struct {
		url     string
		lastMod time.Time
	}
	found := make([]candidate, 0, len(doc.URLs))
	for _, entry := range doc.URLs {
		parsed, err := url.Parse(entry.Loc)
		if err != nil || parsed.Host != r.baseURL.Host {
			continue
		}
		// Posts only: the index page itself shares the prefix.
		if !strings.HasPrefix(parsed.Path, r.prefix) || parsed.Path == r.prefix {
			continue
		}
		when, _ := time.Parse(time.RFC3339, entry.LastMod)
		found = append(found, candidate{url: entry.Loc, lastMod: when})
	}

	slices.SortFunc(found, func(a, b candidate) int { return b.lastMod.Compare(a.lastMod) })

	want := limit * candidateMultiplier
	if want > maxCandidates {
		want = maxCandidates
	}
	if want > len(found) {
		want = len(found)
	}

	urls := make([]string, 0, want)
	for _, c := range found[:want] {
		urls = append(urls, c.url)
	}
	return urls, nil
}

// fetchAll reads candidate pages concurrently.
//
// A page that cannot be read is skipped rather than failing the run: one
// unavailable post should not stop the digest from filing the others.
func (r *Reader) fetchAll(ctx context.Context, urls []string) []Post {
	var (
		mu    sync.Mutex
		posts []Post
		wg    sync.WaitGroup
	)
	sem := make(chan struct{}, fetchConcurrency)

	for _, u := range urls {
		wg.Add(1)
		go func(pageURL string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			post, err := r.fetchOne(ctx, pageURL)
			if err != nil {
				return
			}
			mu.Lock()
			posts = append(posts, post)
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	return posts
}

// jsonLDBlock matches an embedded schema.org script.
var jsonLDBlock = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

// blogPosting is the subset of schema.org BlogPosting this package reads.
type blogPosting struct {
	Type          string `json:"@type"`
	Headline      string `json:"headline"`
	Description   string `json:"description"`
	URL           string `json:"url"`
	DatePublished string `json:"datePublished"`
}

// fetchOne reads a post page and extracts its structured data.
func (r *Reader) fetchOne(ctx context.Context, pageURL string) (Post, error) {
	body, err := r.get(ctx, pageURL, maxPageBytes)
	if err != nil {
		return Post{}, err
	}

	for _, match := range jsonLDBlock.FindAllSubmatch(body, -1) {
		var posting blogPosting
		if err := json.Unmarshal(match[1], &posting); err != nil {
			continue
		}
		if posting.Type != "BlogPosting" || posting.Headline == "" {
			continue
		}

		published, _ := time.Parse(time.RFC3339, posting.DatePublished)
		link := posting.URL
		if link == "" {
			link = pageURL
		}
		return Post{
			URL:         link,
			Title:       html(posting.Headline),
			PublishedAt: published,
			Body:        html(posting.Description),
		}, nil
	}
	return Post{}, fmt.Errorf("blog: no BlogPosting data at %s", pageURL)
}

// get performs a bounded GET.
func (r *Reader) get(ctx context.Context, target string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("blog: build request: %w", err)
	}
	req.Header.Set("User-Agent", "IdentityHub-NHI-Digest/1.0")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("blog: fetch %s: %w", target, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blog: %s returned %d", target, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
}

// html unescapes the few entities that appear in structured data and collapses
// whitespace, so the text reads correctly inside a ticket.
func html(s string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", `"`, "&#39;", "'", "&nbsp;", " ",
	)
	return strings.Join(strings.Fields(replacer.Replace(s)), " ")
}
