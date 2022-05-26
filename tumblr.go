package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const TumblrDate = "Mon, 2 Jan 2006 15:04:05 -0700"

func NewTumblrRSS(ctx context.Context, name string, _ Search) (Feed, error) {
	nameIdx := strings.Index(name, "@")
	if nameIdx != -1 {
		name = name[:nameIdx]
	}
	rssURL := fmt.Sprintf("https://%s.tumblr.com/rss", name)
	req, err := http.NewRequestWithContext(ctx, "GET", rssURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %q: %w", name, err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("download: wrong response code: %d", resp.StatusCode)
	}

	if strings.HasPrefix(resp.Request.URL.Host, "www.tumblr.com") {
		return nil, fmt.Errorf("download: was redirected, feed likely private (%s)", resp.Request.URL)
	}

	var description string

	// TODO: use regular feed reader instead (slowness may come from here?  should actually test this theory)
	dec := xml.NewDecoder(resp.Body)
	token, err := dec.Token()
	for err == nil {
		if el, ok := token.(xml.EndElement); ok && el.Name.Local == "link" {
			break
		}

		if el, ok := token.(xml.StartElement); ok && el.Name.Local == "description" {
			token, err = dec.Token()
			if err != nil {
				break
			}

			if desc, ok := token.(xml.CharData); ok && desc != nil {
				description = string(desc)
			}
		}
		token, err = dec.Token()
	}
	if err != nil && err != io.EOF {
		resp.Body.Close()
		return nil, fmt.Errorf("skip token: %w", err)
	}

	return &tumblrRSS{name: name, description: description, r: resp.Body, dec: dec, dateFormat: TumblrDate}, nil
}

type tumblrRSS struct {
	name        string
	description string
	r           io.ReadCloser
	dec         *xml.Decoder
	dateFormat  string
}

func (tr *tumblrRSS) Name() string {
	return tr.name
}

func (tr *tumblrRSS) Description() string {
	return tr.description
}

func (tr *tumblrRSS) URL() string {
	return fmt.Sprintf("https://%s.tumblr.com/rss", tr.name)
}

var tumblrPostURLRE = regexp.MustCompile(`https?://([-\w]+).tumblr.com/post/(\d+)(/(.*))?`)
var tumblrQuestionRE = regexp.MustCompile(`\s*<p>`)

func (tr *tumblrRSS) Next() (*Post, error) {
	var post Post
	err := tr.dec.Decode(&post)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	post.Source = "tumblr"

	if tumblrPostURLRE.MatchString(post.ID) {
		parts := tumblrPostURLRE.FindStringSubmatch(post.ID)
		if len(parts) >= 3 {
			post.ID = parts[2]
		}
	}

	post.Author = tr.name

	t, dateErr := time.Parse(tr.dateFormat, post.DateString)
	if dateErr != nil {
		return nil, fmt.Errorf("invalid date %q: %s", post.DateString, dateErr)
	}
	post.Date = t

	// TODO: improve reblog support (take reblog-from title/description?)

	// format questions properly
	if tumblrQuestionRE.MatchString(post.Title) {
		post.Title = `<blockquote class="question">` + post.Title + `</blockquote>`
	} else if post.Title != "Photo" && !post.IsReblog() {
		post.Title = `<h1>` + post.Title + `</h1>`
	}

	return &post, nil
}

func (tr *tumblrRSS) Close() error {
	return tr.r.Close()
}

func FlattenReblogs(reblogHTML string) (flattenedHTML string, err error) {
	node, err := html.Parse(strings.NewReader(reblogHTML))
	if err != nil {
		return reblogHTML, fmt.Errorf("parse html: %w", err)
	}

	var root *html.Node

	var f func(*html.Node, *html.Node)
	f = func(parent *html.Node, node *html.Node) {
		if isElement(node, "p") && isElement(nextElementSibling(node), "blockquote") { // p blockquote
			reblog := nextElementSibling(node)
			reblogChild := firstElementChild(reblog)
			reblogContent := nextElementSibling(reblogChild)

			if root == nil {
				root = reblog.Parent
			}

			if isElement(reblogChild, "p") && isElement(reblogContent, "blockquote") { // p blockquote > (p blockquote)
				if parent != nil {
					parent.RemoveChild(node)
				}

				reblogContent.Parent.InsertBefore(node, reblogContent.NextSibling)

				f(reblog, reblogChild)
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			f(node, child)
		}
	}
	f(nil, node)

	if root == nil {
		return reblogHTML, fmt.Errorf("invalid reblog structure: %q", reblogHTML)
	}

	buf := new(bytes.Buffer)
	for node := root; node != nil; node = node.NextSibling {
		err = html.Render(buf, root)
		if err != nil {
			return reblogHTML, fmt.Errorf("render html: %w", err)
		}
	}

	return buf.String(), nil
}

func nextElementSibling(node *html.Node) *html.Node {
	if node == nil {
		return nil
	}

	for next := node.NextSibling; next != nil; next = next.NextSibling {
		if next.Type == html.ElementNode {
			return next
		}
	}
	return nil
}

func firstElementChild(node *html.Node) *html.Node {
	for next := node.FirstChild; next != nil; next = next.NextSibling {
		if next.Type == html.ElementNode {
			return next
		}
	}
	return nil
}

func isElement(node *html.Node, element string) bool {
	return node != nil && node.Type == html.ElementNode && node.Data == element
}
