package rss

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"github.com/heyLu/numblr/feed"
	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"
)

func init() {
	if http.DefaultClient.Jar == nil {
		cookiesPerDomain := make(map[string][]*http.Cookie)
		cookiesPerDomain["livejournal.com"] = []*http.Cookie{
			{Name: "adult_explicit", Value: "1"},
		}
		http.DefaultClient.Jar = domainCookieJar(cookiesPerDomain)
	}
}

var relAlternateMatcher = cascadia.MustCompile(`link[rel=alternate]`)

// Open opens the RSS feed at `name`, trying to find it automatically using
// `rel=alternate` links.
func Open(ctx context.Context, name string, _ feed.Search) (feed.Feed, error) {
	feedURL := name
	if strings.Contains(name, "@") {
		parts := strings.SplitN(name, "@", 2)
		if len(parts) == 0 {
			return nil, fmt.Errorf("unrecognized feed %q", name)
		}
		feedURL = parts[1] + "/@" + parts[0]
	}

	if !strings.HasPrefix(feedURL, "http") {
		feedURL = "http://" + feedURL
	}

	baseURL, err := url.Parse(feedURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", feedURL, err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, feed.StatusError{Code: resp.StatusCode}
	}

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading: %w", err)
	}
	r := strings.NewReader(buf.String())

	feedType := gofeed.DetectFeedType(r)
	_, err = r.Seek(0, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("rewind: %w", err)
	}

	if feedType == gofeed.FeedTypeUnknown {
		node, err := html.Parse(r)
		if err != nil {
			return nil, fmt.Errorf("parse html: %w", err)
		}

		nodes := cascadia.QueryAll(node, relAlternateMatcher)
		found := false
		var url string
		for _, alternate := range nodes {
			if hasAttribute(alternate, "type", "application/atom+xml") || hasAttribute(alternate, "type", "application/rss+xml") {
				found = true
				for _, attr := range alternate.Attr {
					if attr.Key == "href" {
						url = attr.Val
						break
					}
				}
				break
			}
		}

		if !found {
			return nil, fmt.Errorf("no feed found")
		}

		feedURL, err := baseURL.Parse(url)
		if err != nil {
			return nil, fmt.Errorf("invalid feed url %q: %w", url, err)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", feedURL.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("new request: %w", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("open feed: %w", err)
		}
		defer resp.Body.Close()

		buf := new(bytes.Buffer)
		_, err = io.Copy(buf, resp.Body)
		if err != nil {
			return nil, fmt.Errorf("reading: %w", err)
		}

		r = strings.NewReader(buf.String())
	}

	parser := gofeed.NewParser()
	feed, err := parser.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	return &RSS{name: name, feed: feed}, nil
}

func hasAttribute(node *html.Node, attrName, attrValue string) bool {
	for _, attr := range node.Attr {
		if attr.Key == attrName && attr.Val == attrValue {
			return true
		}
	}
	return false
}

// RSS is a Feed implementation for RSS (and ATOM) feeds.
type RSS struct {
	name string
	feed *gofeed.Feed
	item *gofeed.Item
}

// Name implements Feed.Name.
func (rss *RSS) Name() string {
	return rss.name
}

// Description implements Feed.Description.
func (rss *RSS) Description() string {
	return rss.feed.Description
}

// URL implements Feed.URL.
func (rss *RSS) URL() string {
	return rss.feed.Link
}

// Next implements Feed.Next.
func (rss *RSS) Next() (*feed.Post, error) {
	if len(rss.feed.Items) == 0 {
		return nil, io.EOF
	}

	item := rss.feed.Items[0]
	rss.item = item
	rss.feed.Items = rss.feed.Items[1:]

	var avatarURL string
	if rss.feed.Image != nil {
		avatarURL = rss.feed.Image.URL
	}

	dateString := item.Published
	date := item.PublishedParsed
	if date == nil {
		dateString = item.Updated
		date = item.UpdatedParsed
	}
	if date == nil {
		t := time.Now()
		date = &t
		dateString = date.UTC().Format(time.RFC3339)
	}
	content := item.Content
	if content == "" {
		content = item.Description
	}
	for _, encl := range item.Enclosures {
		if strings.HasPrefix(encl.Type, "image") {
			content += fmt.Sprintf(`<img src="%s" />`, encl.URL)
		}
	}
	return &feed.Post{
		Source:          "web",
		ID:              item.GUID,
		Author:          rss.name,
		AvatarURL:       avatarURL,
		URL:             item.Link,
		Title:           fmt.Sprintf(`<h1>%s</h1>`, item.Title),
		DescriptionHTML: content,
		Tags:            item.Categories,
		DateString:      dateString,
		Date:            *date,
	}, nil
}

// FeedItem returns the current gofeed.Item, as navigated to using `Next`.
func (rss *RSS) FeedItem() *gofeed.Item {
	return rss.item
}

// Close implements Feed.Close.
func (rss *RSS) Close() error {
	return nil
}

type domainCookieJar map[string][]*http.Cookie

func (dcj domainCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {}

func (dcj domainCookieJar) Cookies(u *url.URL) []*http.Cookie {
	cookies, ok := dcj[u.Host]
	if ok {
		return cookies
	}
	domainParts := strings.SplitN(u.Host, ".", 2)
	for i := range domainParts {
		cookies, ok := dcj[strings.Join(domainParts[i:], ".")]
		if ok {
			return cookies
		}
	}
	return nil
}
