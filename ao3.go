package main

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
	"golang.org/x/net/html"
)

var workMatcher = cascadia.MustCompile("li.work")
var dateMatcher = cascadia.MustCompile(".datetime")
var titleMatcher = cascadia.MustCompile(".header .heading a")
var authorMatcher = cascadia.MustCompile(".header .heading a[rel=author]")
var summaryMatcher = cascadia.MustCompile(".summary")
var fandomTagsMatcher = cascadia.MustCompile(".fandoms a.tag")
var requiredTagsMatcher = cascadia.MustCompile(".required-tags li span.text")
var tagsMatcher = cascadia.MustCompile("ul.tags li .tag")

type ao3 struct {
	name string

	works []*html.Node
}

func NewAO3(ctx context.Context, name string, _ Search) (Feed, error) {
	// TODO: implement author@ao3
	// TODO: implement ao3 search

	u, err := url.Parse(name)
	if err != nil {
		return nil, fmt.Errorf("invalid feed url %q: %w", name, err)
	}

	// remove unnecessary data from url
	query := u.Query()
	delete(query, "commit")
	delete(query, "utf8")
	for key, vals := range query {
		if len(vals) == 0 || vals[0] == "" {
			delete(query, key)
		}
	}
	u.RawQuery = query.Encode()

	name, err = url.QueryUnescape(u.String())
	if err != nil {
		return nil, fmt.Errorf("unescape %q: %w", u.String(), err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %q: %w", name, err)
	}
	defer resp.Body.Close()

	node, err := html.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}

	works := cascadia.QueryAll(node, workMatcher)

	return &ao3{
		name:  name,
		works: works,
	}, nil
}

func (ao3 *ao3) Name() string {
	return ao3.name
}

func (ao3 *ao3) URL() string {
	return ao3.name
}

func (ao3 *ao3) Next() (*Post, error) {
	if len(ao3.works) == 0 {
		return nil, io.EOF
	}

	work := ao3.works[0]

	var id string
	for _, attr := range work.Attr {
		if attr.Key == "id" {
			if !strings.HasPrefix(attr.Val, "work_") {
				return nil, fmt.Errorf("invalid id %q", attr.Val)
			}

			id = attr.Val[len("work_"):]
		}
	}
	if id == "" {
		return nil, fmt.Errorf("invalid id %q", id)
	}

	postURL := "https://archiveofourown.org/works/" + id

	title := cascadia.Query(work, titleMatcher)
	if title == nil || title.FirstChild == nil {
		return nil, fmt.Errorf("no title")
	}

	author := cascadia.Query(work, authorMatcher)
	if author == nil || author.FirstChild == nil {
		return nil, fmt.Errorf("no author")
	}

	date := cascadia.Query(work, dateMatcher)
	if dateMatcher == nil {
		return nil, fmt.Errorf("no date")
	}
	dateString := date.FirstChild.Data
	dateParsed, err := time.Parse("2 Jan 2006", dateString)
	if err != nil {
		return nil, fmt.Errorf("invalid date %q: %w", dateString, err)
	}

	descriptionHTML := new(bytes.Buffer)
	for child := work.FirstChild; child != nil; child = child.NextSibling {
		// skip whitespace
		if child.Type == html.TextNode && strings.TrimSpace(child.Data) == "" {
			continue
		}

		if child.Data == "div" || child.Data == "h6" || child.Data == "ul" {
			continue
		}

		makeAbsoluteLinks(child, "https://archiveofourown.org")

		err := html.Render(descriptionHTML, child)
		if err != nil {
			return nil, fmt.Errorf("render summary: %w", err)
		}
	}

	fandomTagNodes := cascadia.QueryAll(work, fandomTagsMatcher)
	if fandomTagNodes == nil || len(fandomTagNodes) == 0 {
		return nil, fmt.Errorf("no fandom tags")
	}
	requiredTagNodes := cascadia.QueryAll(work, requiredTagsMatcher)
	if requiredTagNodes == nil || len(requiredTagNodes) == 0 {
		return nil, fmt.Errorf("no required tags")
	}
	tagNodes := cascadia.QueryAll(work, tagsMatcher)
	if tagNodes == nil || len(tagNodes) == 0 {
		return nil, fmt.Errorf("no tags")
	}
	tags := make([]string, 0, len(fandomTagNodes)+len(requiredTagNodes)+len(tagNodes))
	for _, tagNode := range fandomTagNodes {
		if tagNode.FirstChild == nil {
			return nil, fmt.Errorf("invalid tag")
		}

		tags = append(tags, tagNode.FirstChild.Data)
	}
	seenTag := make(map[string]bool)
	for _, tagNode := range requiredTagNodes {
		if tagNode.FirstChild == nil {
			return nil, fmt.Errorf("invalid tag")
		}

		for _, tag := range strings.Split(tagNode.FirstChild.Data, ", ") {
			seenTag[tag] = true

			tags = append(tags, tag)
		}
	}
	for _, tagNode := range tagNodes {
		if tagNode.FirstChild == nil {
			return nil, fmt.Errorf("invalid tag")
		}

		if seenTag[tagNode.FirstChild.Data] {
			continue
		}

		tags = append(tags, tagNode.FirstChild.Data)
	}

	ao3.works = ao3.works[1:]
	return &Post{
		Source:          "ao3",
		ID:              id,
		URL:             postURL,
		Title:           fmt.Sprintf("<h1><a href=%q>", postURL) + title.FirstChild.Data + "</a> by " + author.FirstChild.Data + "</h1>",
		Author:          author.FirstChild.Data, // FIXME: cannot fetch from db because author != url?
		DescriptionHTML: descriptionHTML.String(),
		Tags:            tags,
		DateString:      dateString,
		Date:            dateParsed.UTC(),
	}, nil
}

func makeAbsoluteLinks(node *html.Node, baseURL string) {
	for i, attr := range node.Attr {
		if attr.Key == "href" && len(attr.Val) > 0 && attr.Val[0] == '/' {
			node.Attr[i].Val = baseURL + attr.Val
		}
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		makeAbsoluteLinks(child, baseURL)
	}
}

func (ao3 *ao3) Close() error {
	return nil
}
