package main

import (
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/gorilla/mux"
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

var imgRE = regexp.MustCompile(`<img `)
var linkRE = regexp.MustCompile(`<a `)
var videoRE = regexp.MustCompile(`<video `)
var autoplayRE = regexp.MustCompile(` autoplay="autoplay"`)

var config struct {
	DefaultTumblr string
}

func main() {
	flag.StringVar(&config.DefaultTumblr, "default", "nettleforest", "Default tumblr to view")
	flag.Parse()

	router := mux.NewRouter()
	router.Use(gziphandler.GzipHandler)

	router.HandleFunc("/favicon.ico", func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	})

	router.HandleFunc("/robots.txt", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintln(w, `User-agent: *
Disallow: /`)
	})

	router.HandleFunc("/", HandleTumblr)
	router.HandleFunc("/{tumblrs}", HandleTumblr)

	http.Handle("/", router)

	addr := "localhost:5555"
	log.Printf("Listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func HandleTumblr(w http.ResponseWriter, req *http.Request) {
	start := time.Now()

	tumbl := tumblsFromRequest(req)

	search := req.URL.Query().Get("search")

	w.Header().Set("Content-Type", "text/html")

	modeCSS := ""
	if _, ok := req.URL.Query()["night-mode"]; ok {
		modeCSS = `body { color: #bbb; background-color: #333; }`
	}
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width,minimum-scale=1,initial-scale=1" />
	<meta name="description" content="Mirror of %s tumblrs" />
	<title>%s</title>
	<style>h1 { word-break: break-all; }blockquote, figure { margin-left: 0; }body { font-family: sans-serif; }article{ border-bottom: 1px solid black; padding: 1em 0; }.tags { list-style: none; padding: 0; font-size: smaller; color: #666; }.tags > li { display: inline }img, video { max-width: 95vw; }@media (min-width: 60em) { body { margin-left: 15vw; } article { max-width: 60em; } img, video { max-height: 20vh; } img:hover, video:hover { max-height: 100%%; }}%s</style>
	<link rel="preconnect" href="https://64.media.tumblr.com/" />
</head>

<body>

<h1>%s</h1>

`, tumbl, tumbl, modeCSS, tumbl)

	fmt.Fprintf(w, `<form method="GET" action=%q><input aria-label="search posts" name="search" type="search" value=%q placeholder="noreblog #art ..." /></form>`, req.URL.Path, search)

	multiple := false
	var tumblr Tumblr
	var err error
	if strings.Contains(tumbl, ",") {
		multiple = true
		tumbls := strings.Split(tumbl, ",")
		tumblrs := make([]Tumblr, len(tumbls))
		var wg sync.WaitGroup
		wg.Add(len(tumbls))
		for i := range tumbls {
			go func(i int) {
				tumblrs[i], err = NewTumblrRSS(tumbls[i])
				wg.Done()
			}(i)
		}
		wg.Wait()
		tumblr = MergeTumblrs(tumblrs...)
	} else {
		tumblr, err = NewTumblrRSS(tumbl)
	}
	if err != nil {
		log.Println("open: ", err)
		if tumblr == nil {
			fmt.Fprintf(w, `<code style="color: red; font-weight: bold; font-size: larger;">could not load tumblr: %s</code>`, err)
			return
		}
	}
	defer tumblr.Close()
	openTime := time.Since(start)

	postCount := 0
	var post *Post
	nextPost := func() {
		post, err = tumblr.Next()
	}

	imageCount := 0
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

		postHTML := html.UnescapeString(post.DescriptionHTML)
		// load first 5 images eagerly, and the rest lazily
		postHTML = imgRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			imageCount++
			if imageCount > 5 {
				return `<img loading="lazy" `
			}
			return `<img `
		})
		postHTML = linkRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			return `<a rel="noreferrer" `
		})
		postHTML = videoRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			return `<video preload="metadata" `
		})
		postHTML = autoplayRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			return ``
		})

		fmt.Fprint(w, postHTML)

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
		log.Println("decode: ", err)
	}
}

func tumblsFromRequest(req *http.Request) string {
	// explicitely specified
	tumbl := req.URL.Path[1:]
	if tumbl != "" {
		return tumbl
	}

	cookie, err := req.Cookie("numbl")
	if err != nil {
		if err != http.ErrNoCookie {
			log.Printf("getting cookie: %s", err)
		}
		return config.DefaultTumblr
	}

	if cookie.Value != "" {
		return cookie.Value
	}

	return config.DefaultTumblr
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
		return nil, fmt.Errorf("download %q: %w", name, err)
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
