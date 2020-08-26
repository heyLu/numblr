package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const TumblrDate = "Mon, 2 Jan 2006 15:04:05 -0700"

type Post struct {
	Author          string
	URL             string   `xml:"link"`
	Title           string   `xml:"title"`
	DescriptionHTML string   `xml:"description"`
	Tags            []string `xml:"category"`
	DateString      string   `xml:"pubDate"`
	Date            time.Time
}

func (p Post) IsReblog() bool {
	return strings.Contains(p.DescriptionHTML, `class="tumblr_blog"`)
}

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/favicon.ico" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		tumbl := req.URL.Path[1:]
		if tumbl == "" {
			tumbl = "nettleforest"
		}

		search := req.URL.Query().Get("search")

		start := time.Now()
		multiple := false
		var tumblr Tumblr
		var err error
		if strings.Contains(tumbl, ",") {
			multiple = true
			tumbls := strings.Split(tumbl, ",")
			tumblrs := make([]Tumblr, len(tumbls))
			var wg sync.WaitGroup
			wg.Add(len(tumbls))
			for i, tumbl := range tumbls {
				func() {
					tumblrs[i], err = NewTumblrRSS(tumbl)
					wg.Done()
				}()
			}
			wg.Wait()
			tumblr = MergeTumblrs(tumblrs...)
		} else {
			tumblr, err = NewTumblrRSS(tumbl)
		}
		if err != nil {
			log.Fatal("open:", err)
		}
		defer tumblr.Close()
		openTime := time.Since(start)

		w.Header().Set("Content-Type", "text/html")

		modeCSS := ""
		if _, ok := req.URL.Query()["night-mode"]; ok {
			modeCSS = `body { color: #bbb; background-color: #333; }`
		}
		fmt.Fprintf(w, `<!doctype html>
<html>
<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width,minimum-scale=1,initial-scale=1" />
	<title>%s</title>
	<style>blockquote { margin-left: 0; }body { font-family: sans-serif; }article{ border-bottom: 1px solid black; padding: 1em 0; }.tags { list-style: none; padding: 0; font-size: smaller; color: #666; }.tags > li { display: inline }img { max-width: 95vw; }@media (min-width: 60em) { body { margin-left: 15vw; } article { max-width: 60em; } img { max-height: 20vh; } img:hover { max-height: 100%%; }}%s</style>
	<link rel="preconnect" href="https://64.media.tumblr.com/" />
</head>

<body>`, tumbl, modeCSS)

		fmt.Fprintf(w, `<form method="GET" action=%q><input name="search" value=%q /></form>`, req.URL.Path, search)

		postCount := 0
		var post *Post
		nextPost := func() {
			post, err = tumblr.Next()
		}

		nextPost()
		for err == nil {
			classes := make([]string, 0, 1)
			if post.IsReblog() {
				if strings.Contains(search, "no-reblog") {
					nextPost()
					continue
				}
				classes = append(classes, "reblog")
			}
			postCount++
			fmt.Fprintf(w, `<article class=%q>`, strings.Join(classes, " "))
			if multiple {
				fmt.Fprintf(w, "<p>%s:<p>", post.Author)
			}
			fmt.Fprint(w, html.UnescapeString(post.DescriptionHTML))
			fmt.Fprint(w, "<footer>")
			if len(post.Tags) > 0 {
				fmt.Fprint(w, `<ul class="tags">`)
				for _, tag := range post.Tags {
					fmt.Fprintf(w, `<li>#%s</li> `, tag)
				}
				fmt.Fprintln(w, `</ul>`)
			}
			fmt.Fprintf(w, `<time title="%s" datetime="%s">%s ago</time>, <a href=%q>link</a>`, post.Date, post.DateString, time.Since(post.Date).Round(time.Minute), post.URL)
			fmt.Fprint(w, "</footer>")
			fmt.Fprintln(w, "</article>")

			nextPost()
		}
		fmt.Fprintf(w, `<footer>%d posts from %q (<a href=%q>source</a>) in %s (open: %s)</footer>`, postCount, tumblr.Name(), tumblr.URL(), time.Since(start).Round(time.Millisecond), openTime.Round(time.Millisecond))
		if err != nil && !errors.Is(err, io.EOF) {
			log.Fatal("decode:", err)
		}
	})

	addr := "localhost:5555"
	log.Printf("Listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func MergeTumblrs(tumblrs ...Tumblr) Tumblr {
	return &tumblrMerger{tumblrs: tumblrs, posts: make([]*Post, len(tumblrs)), errors: make([]error, len(tumblrs))}
}

type tumblrMerger struct {
	tumblrs []Tumblr
	posts   []*Post
	errors  []error
}

func (tm *tumblrMerger) Name() string {
	name := ""
	for _, t := range tm.tumblrs {
		name += " " + t.Name()
	}
	return name
}

func (tm *tumblrMerger) URL() string {
	return ""
}

func (tm *tumblrMerger) Next() (*Post, error) {
	allErrors := false
	for _, err := range tm.errors {
		allErrors = allErrors && err != nil
	}
	if allErrors {
		return nil, tm.errors[0]
	}

	var wg sync.WaitGroup
	wg.Add(len(tm.tumblrs))
	for i := range tm.tumblrs {
		go func(i int) {
			if tm.posts[i] == nil && !errors.Is(tm.errors[i], io.EOF) {
				tm.posts[i], tm.errors[i] = tm.tumblrs[i].Next()
			}
			wg.Done()
		}(i)
	}
	wg.Wait()

	postIdx := -1
	var firstPost *Post

	for i, post := range tm.posts {
		if post == nil {
			continue
		}

		if firstPost == nil || post.Date.After(firstPost.Date) {
			postIdx = i
			firstPost = post
		}
	}

	if firstPost == nil {
		return nil, fmt.Errorf("no more posts: %w", io.EOF)
	}

	tm.posts[postIdx] = nil
	firstPost.Author = tm.tumblrs[postIdx].Name()
	return firstPost, nil
}

func (tm *tumblrMerger) Close() error {
	var err error
	for _, t := range tm.tumblrs {
		err = t.Close()
	}
	return err
}

type Tumblr interface {
	Name() string
	URL() string
	Next() (*Post, error)
	Close() error
}

func NewTumblrRSS(name string) (Tumblr, error) {
	rssURL := fmt.Sprintf("https://%s.tumblr.com/rss", name)
	resp, err := http.Get(rssURL)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download: wrong response code: %d", resp.StatusCode)
	}

	dec := xml.NewDecoder(resp.Body)
	token, err := dec.Token()
	for err == nil {
		el, ok := token.(xml.EndElement)
		if ok && el.Name.Local == "link" {
			break
		}
		token, err = dec.Token()
	}
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("skip token: %w", err)
	}

	return &tumblrRSS{name: name, r: resp.Body, dec: dec}, nil
}

type tumblrRSS struct {
	name string
	r    io.ReadCloser
	dec  *xml.Decoder
}

func (tr *tumblrRSS) Name() string {
	return tr.name
}

func (tr *tumblrRSS) URL() string {
	return fmt.Sprintf("https://%s.tumblr.com/rss", tr.name)
}

func (tr *tumblrRSS) Next() (*Post, error) {
	var post Post
	err := tr.dec.Decode(&post)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	post.Author = tr.name

	t, dateErr := time.Parse(TumblrDate, post.DateString)
	if dateErr != nil {
		return nil, fmt.Errorf("invalid date %q: %s", post.DateString, dateErr)
	}
	post.Date = t

	return &post, err
}

func (tr *tumblrRSS) Close() error {
	return tr.r.Close()
}
