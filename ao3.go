package main

import (
	"bytes"
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
var tagsMatcher = cascadia.MustCompile("ul.tags li .tag")

type ao3 struct {
	name string

	works []*html.Node
}

func NewAO3(name string, _ Search) (Tumblr, error) {
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

	resp, err := http.Get(u.String())
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

	summary := cascadia.Query(work, summaryMatcher)
	if summary == nil {
		return nil, fmt.Errorf("no summary")
	}
	descriptionHTML := new(bytes.Buffer)
	for child := summary.FirstChild; child != nil; child = child.NextSibling {
		// skip whitespace
		if child.Type == html.TextNode && strings.TrimSpace(child.Data) == "" {
			continue
		}

		err := html.Render(descriptionHTML, child)
		if err != nil {
			return nil, fmt.Errorf("render summary: %w", err)
		}
	}

	tagNodes := cascadia.QueryAll(work, tagsMatcher)
	if tagNodes == nil || len(tagNodes) == 0 {
		return nil, fmt.Errorf("no tags")
	}
	tags := make([]string, 0, len(tagNodes))
	for _, tagNode := range tagNodes {
		if tagNode.FirstChild == nil {
			return nil, fmt.Errorf("invalid tag")
		}

		tags = append(tags, tagNode.FirstChild.Data)
	}

	ao3.works = ao3.works[1:]
	return &Post{
		Source:          "ao3",
		ID:              id,
		URL:             postURL,
		Title:           fmt.Sprintf("<h1><a href=%q>", postURL) + title.FirstChild.Data + "</a> by " + author.FirstChild.Data + "</h1>",
		Author:          author.FirstChild.Data,
		DescriptionHTML: descriptionHTML.String(),
		Tags:            tags,
		DateString:      dateString,
		Date:            dateParsed.UTC(),
	}, nil
}

func (ao3 *ao3) Close() error {
	return nil
}
