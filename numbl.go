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
	"github.com/hashicorp/golang-lru"
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

const CookieName = "numbl"

var config struct {
	DefaultTumblr string
}

const CacheTime = 10 * time.Minute

var cache *lru.Cache

func main() {
	flag.StringVar(&config.DefaultTumblr, "default", "nettleforest", "Default tumblr to view")
	flag.Parse()

	var err error
	cache, err = lru.New(100)
	if err != nil {
		log.Fatal("setup cache:", err)
	}

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

	router.HandleFunc("/settings", func(w http.ResponseWriter, req *http.Request) {
		tumblrs := req.FormValue("tumblrs")

		first := true
		cookieValue := ""
		for _, tumblr := range strings.Split(tumblrs, "\n") {
			tumblr = strings.TrimSpace(tumblr)
			if tumblr == "" {
				continue
			}
			if !first {
				cookieValue += ","
			}
			first = false
			cookieValue += tumblr
		}

		if cookieValue == "" {
			http.Redirect(w, req, "/", http.StatusTemporaryRedirect)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     CookieName,
			Value:    cookieValue,
			SameSite: http.SameSiteStrictMode,
			HttpOnly: true,
		})
		http.Redirect(w, req, "/", http.StatusSeeOther)
	}).Methods("POST")

	router.HandleFunc("/settings/clear", func(w http.ResponseWriter, req *http.Request) {
		cookie, err := req.Cookie(CookieName)
		if err != nil {
			http.Redirect(w, req, "/", http.StatusTemporaryRedirect)
			return
		}

		cookie.Value = ""
		cookie.MaxAge = -1
		http.SetCookie(w, cookie)
		http.Redirect(w, req, "/", http.StatusSeeOther)
	}).Methods("POST")

	router.HandleFunc("/{tumblrs}", HandleTumblr)
	router.HandleFunc("/", HandleTumblr)

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
				tumblrs[i], err = NewCachedTumblr(tumbls[i], NewTumblrRSS)
				wg.Done()
			}(i)
		}
		wg.Wait()
		tumblr = MergeTumblrs(tumblrs...)
	} else {
		tumblr, err = NewCachedTumblr(tumbl, NewTumblrRSS)
	}
	if err != nil {
		log.Println("open:", err)
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

	tumbls := strings.Split(tumbl, ",")
	fmt.Fprintf(w, `<form method="POST" action="/settings">
	<label for="tumblrs">Tumblrs to view by default</label>:

	<div class="field">
		<textarea rows="%d" cols="30" name="tumblrs">%s</textarea>
	</div>
	<input type="submit" value="Save" />
</form>

<form method="POST" action="/settings/clear">
	<input type="submit" value="Clear" title="FIXME: clear currently broken :/" disabled />
</form>
`, len(tumbls)+1, strings.Join(tumbls, "\n"))

	fmt.Fprintf(w, `<hr /><footer>%d posts from %q (<a href=%q>source</a>) in %s (open: %s)</footer>`, postCount, tumblr.Name(), tumblr.URL(), time.Since(start).Round(time.Millisecond), openTime.Round(time.Millisecond))
	if err != nil && !errors.Is(err, io.EOF) {
		log.Println("decode:", err)
	}

	fmt.Fprintln(w, `<script>
  let startY;

  window.addEventListener('touchstart', e => {
    startY = e.touches[0].pageY;
  }, {passive: true});

  window.addEventListener('touchmove', e => {
    const y = e.touches[0].pageY;
    if (document.scrollingElement.scrollTop === 0 && y > startY) {
      window.location.reload();
    }
  }, {passive: true});
</script>`)

	fmt.Fprintln(w, `</body>
</html>`)
}

func tumblsFromRequest(req *http.Request) string {
	// explicitely specified
	tumbl := req.URL.Path[1:]
	if tumbl != "" {
		return tumbl
	}

	cookie, err := req.Cookie(CookieName)
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

func NewCachedTumblr(name string, uncachedFn func(name string) (Tumblr, error)) (Tumblr, error) {
	cached, isCached := cache.Get(name)
	if isCached && time.Since(cached.(*cachedTumblr).cachedAt) < CacheTime {
		tumblr := *cached.(*cachedTumblr)
		return &tumblr, nil
	}
	tumblr, err := uncachedFn(name)
	if err != nil {
		return nil, err
	}
	return &cachingTumblr{
		uncached: tumblr,
		cached: &cachedTumblr{
			cachedAt: time.Now(),
			name:     name,
			url:      tumblr.URL(),
			posts:    make([]*Post, 0, 10),
		},
	}, nil
}

type cachingTumblr struct {
	uncached Tumblr
	cached   *cachedTumblr
}

func (ct *cachingTumblr) Name() string {
	return ct.uncached.Name()
}

func (ct *cachingTumblr) URL() string {
	return ct.uncached.URL()
}

func (ct *cachingTumblr) Next() (*Post, error) {
	post, err := ct.uncached.Next()
	if err != nil {
		return nil, err
	}
	ct.cached.posts = append(ct.cached.posts, post)
	return post, nil
}

func (ct *cachingTumblr) Close() error {
	cache.Add(ct.uncached.Name(), ct.cached)
	return ct.uncached.Close()
}

type cachedTumblr struct {
	cachedAt time.Time
	name     string
	url      string
	posts    []*Post
}

func (ct *cachedTumblr) Name() string {
	return ct.name
}

func (ct *cachedTumblr) URL() string {
	return ct.url
}

func (ct *cachedTumblr) Next() (*Post, error) {
	if len(ct.posts) == 0 {
		return nil, fmt.Errorf("no more posts: %w", io.EOF)
	}

	post := ct.posts[0]
	ct.posts = ct.posts[1:]
	return post, nil
}

func (ct *cachedTumblr) Close() error {
	return nil
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
