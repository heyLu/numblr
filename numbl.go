package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/gorilla/mux"
	"github.com/hashicorp/golang-lru"
	"golang.org/x/net/html"
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

var isReblogRE = regexp.MustCompile(`\s*[-_a-zA-Z0-9]+:`)

func (p Post) IsReblog() bool {
	return isReblogRE.MatchString(p.Title) || strings.Contains(p.DescriptionHTML, `class="tumblr_blog"`)
}

var imgRE = regexp.MustCompile(`<img `)
var widthHeightRE = regexp.MustCompile(` (width|height|style)="[^"]+"`)
var linkRE = regexp.MustCompile(`<a `)
var tumblrLinksRE = regexp.MustCompile(`https?://([^.]+).tumblr.com([^" ]+)?`)
var videoRE = regexp.MustCompile(`<video `)
var autoplayRE = regexp.MustCompile(` autoplay="autoplay"`)

const CookieName = "numbl"

var config struct {
	DefaultTumblr string
}

const CacheTime = 10 * time.Minute
const AvatarSize = 64
const AvatarCacheTime = 30 * 24 * time.Hour

var cache *lru.Cache
var avatarCache *lru.Cache

var httpClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func main() {
	flag.StringVar(&config.DefaultTumblr, "default", "nettleforest", "Default tumblr to view")
	flag.Parse()

	var err error
	cache, err = lru.New(100)
	if err != nil {
		log.Fatal("setup cache:", err)
	}
	avatarCache, err = lru.New(100)
	if err != nil {
		log.Fatal("setup avatar cache:", err)
	}

	router := mux.NewRouter()
	router.Use(gziphandler.GzipHandler)

	router.HandleFunc("/favicon.png", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(FaviconPNGBytes)
		return
	})

	router.HandleFunc("/robots.txt", func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintln(w, `User-agent: *
Disallow: /`)
	})

	router.HandleFunc("/manifest.webmanifest", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")

		fmt.Fprintln(w, `{
  "name": "Numblr",
  "description": "A bare-bones mirror for tumblrs.",
  "short_name": "numblr",
  "lang": "en",
  "start_url": "/",
  "icons": [{
    "src": "/favicon.png",
    "sizes": "192x192",
	 "purpose": "any maskable",
	 "type": "image/png"
  }],
  "display": "standalone",
  "orientation": "portrait",
  "background_color": "#222222",
  "theme_color": "#222222"
}`)
	})
	// required to be registered as a progressive web app (?)
	router.HandleFunc("/service-worker.js", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		fmt.Fprint(w, `// required to be registered as a progressive web app
// may cache things later, or provide notifications

self.addEventListener('install', function(e) {
  e.waitUntil(Promise.resolve()).then(() => {
    console.log("numblr installed!");
  });
});
`)
	})

	router.HandleFunc("/settings", func(w http.ResponseWriter, req *http.Request) {
		list := req.FormValue("list")
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

		cookieName := CookieName
		if list != "" {
			cookieName = CookieName + "-list-" + list
		}

		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    cookieValue,
			MaxAge:   365 * 24 * 60 * 60, // one year
			SameSite: http.SameSiteLaxMode,
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

	router.HandleFunc("/avatar/{tumblr}", HandleAvatar)

	router.HandleFunc("/{tumblrs}", HandleTumblr)
	router.HandleFunc("/", HandleTumblr)

	router.HandleFunc("/{tumblr}/post/{postId}", HandlePost)
	router.HandleFunc("/{tumblr}/post/{postId}/{slug}", HandlePost)

	http.Handle("/", router)

	addr := "localhost:5555"
	log.Printf("Listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func HandleAvatar(w http.ResponseWriter, req *http.Request) {
	tumblr := mux.Vars(req)["tumblr"]

	avatar, isCached := avatarCache.Get(tumblr)
	if isCached {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, immutable", int(AvatarCacheTime.Seconds())))
		w.Write(avatar.([]byte))
		return
	}

	var avatarURL string
	switch {
	case strings.Contains(tumblr, "@"):
		http.Error(w, fmt.Sprintf("Error: fetching avatar for %q not supported", tumblr), http.StatusInternalServerError)
		return
	case strings.Contains(tumblr, "."):
		avatarURL = "http://" + tumblr + "/favicon.ico"
	default:
		avatarURL = fmt.Sprintf("https://api.tumblr.com/v2/blog/%s.tumblr.com/avatar/%d", url.PathEscape(tumblr), AvatarSize)
	}

	resp, err := http.Get(avatarURL)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: fetching avatar: %s", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		http.Error(w, fmt.Sprintf("Error: fetching avatar: unexpected status code %d", resp.StatusCode), http.StatusInternalServerError)
		return
	}

	buf := new(bytes.Buffer)
	wr := io.MultiWriter(w, buf)

	avatar = resp.Header.Get("Location")
	w.Header().Set("Cache-Control", fmt.Sprintf("max-age=%d", int(AvatarCacheTime.Seconds())))

	_, err = io.Copy(wr, resp.Body)
	if err != nil {
		log.Printf("could not write avatar: %s", err)
		return
	}

	avatarCache.Add(tumblr, buf.Bytes())
}

func HandleTumblr(w http.ResponseWriter, req *http.Request) {
	start := time.Now()

	tumbl := tumblsFromRequest(req)

	search := req.URL.Query().Get("search")

	w.Header().Set("Content-Type", "text/html")

	nightModeCSS := `body { color: #fff; background-color: #222; }.tags { color: #b7b7b7 }a { color: pink; }a:visited { color: #a67070; }article{ border-bottom: 1px solid #666; }blockquote:not(:last-child) { border-bottom: 1px solid #333; }a.author,a.author:visited{color: #fff;}`
	modeCSS := `@media (prefers-color-scheme: dark) {` + nightModeCSS + `}`
	if _, ok := req.URL.Query()["night-mode"]; ok {
		modeCSS = nightModeCSS
	}
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width,minimum-scale=1,initial-scale=1" />
	<meta name="description" content="Mirror of %s tumblrs" />
	<title>%s</title>
	<style>h1 { word-break: break-all; }blockquote, figure { margin: 0; }blockquote:not(:last-child) { border-bottom: 1px solid #ddd; } blockquote > blockquote:nth-child(1) { border-bottom: 0; }body { font-family: sans-serif; }article{ border-bottom: 1px solid black; padding: 1em 0; }.tags { list-style: none; padding: 0; color: #666; }.tags > li { display: inline }img, video, iframe { max-width: 95vw; }@media (min-width: 60em) { body { margin-left: 15vw; } article { max-width: 60em; } img, video { max-height: 20vh; } img:hover, video:hover { max-height: 100%%; }}.avatar{height: 1em;}a.author,a.author:visited{color: #000;}%s</style>
	<link rel="preconnect" href="https://64.media.tumblr.com/" />
	<link rel="manifest" href="/manifest.webmanifest" />
	<meta name="theme_color" content="#222222" />
	<link rel="icon" href="/favicon.png" />
</head>

<body>

<h1>%s</h1>

`, tumbl, tumbl, modeCSS, tumbl)

	fmt.Fprintf(w, `<form method="GET" action=%q><input aria-label="search posts" name="search" type="search" value=%q placeholder="noreblog #art ..." /></form>`, req.URL.Path, search)

	var tumblr Tumblr
	var err error
	if strings.Contains(tumbl, ",") {
		tumbls := strings.Split(tumbl, ",")
		tumblrs := make([]Tumblr, len(tumbls))
		var wg sync.WaitGroup
		wg.Add(len(tumbls))
		for i := range tumbls {
			go func(i int) {
				tumblrs[i], err = NewCachedFeed(tumbls[i])
				if err != nil {
					err = fmt.Errorf("%s: %w", tumbls[i], err)
				}
				wg.Done()
			}(i)
		}
		wg.Wait()
		successfulTumblrs := make([]Tumblr, 0, len(tumbls))
		for _, tumblr := range tumblrs {
			if tumblr == nil {
				continue
			}
			successfulTumblrs = append(successfulTumblrs, tumblr)
		}
		tumblr = MergeTumblrs(successfulTumblrs...)
	} else {
		tumblr, err = NewCachedFeed(tumbl)
	}
	if err != nil {
		log.Println("open:", err)
		fmt.Fprintf(w, `<code style="color: red; font-weight: bold; font-size: larger;">could not load tumblr: %s</code>`, err)
		if tumblr == nil {
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
		fmt.Fprintf(w, `<p><img class="avatar" src="/avatar/%s" /> <a class="author" href="/%s">%s</a>:<p>`, post.Author, post.Author, post.Author)

		fmt.Fprintln(w, `<section class="post-content">`)

		postHTML := ""
		if post.Title != "Photo" && !post.IsReblog() {
			postHTML = html.UnescapeString(post.Title)
		}
		postHTML += html.UnescapeString(post.DescriptionHTML)
		// load first 5 images eagerly, and the rest lazily
		postHTML = imgRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			imageCount++
			if imageCount > 5 {
				return `<img loading="lazy" `
			}
			return `<img `
		})
		postHTML = widthHeightRE.ReplaceAllString(postHTML, ` `)
		postHTML = linkRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			return `<a rel="noreferrer" `
		})
		postHTML = tumblrLinksRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			u, err := url.Parse(repl)
			if err != nil {
				log.Printf("could not parse url: %s", err)
				return repl
			}

			tumblrName := u.Host[:strings.Index(u.Host, ".")]
			u.Host = ""
			u.Scheme = ""
			u.Path = path.Join(tumblrName, u.Path)
			return u.String()
		})
		postHTML = videoRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			return `<video preload="metadata" controls="" `
		})
		postHTML = autoplayRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			return ``
		})

		fmt.Fprint(w, postHTML)

		fmt.Fprintln(w, `</section>`)

		fmt.Fprint(w, "<footer>")
		if len(post.Tags) > 0 {
			fmt.Fprint(w, `<ul class="tags">`)
			for _, tag := range post.Tags {
				fmt.Fprintf(w, `<li>#%s</li> `, tag)
			}
			fmt.Fprintln(w, `</ul>`)
		}
		fmt.Fprintf(w, `<time title="%s" datetime="%s">%s ago</time>, <a href=%q>link</a>`, post.Date, post.DateString, prettyDuration(time.Since(post.Date)), post.URL)
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

  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/service-worker.js')
      .then(() => console.log("numblr registered!"))
		.catch((err) => console.log("numblr registration failed: ", err));
  }
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

func prettyDuration(dur time.Duration) string {
	switch {
	case dur < 24*time.Hour:
		return dur.Round(time.Minute).String()
	case dur < 30*24*time.Hour:
		days := (int(dur.Hours()) / 24)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	case dur < 365*24*time.Hour:
		months := (int(dur.Hours()) / 24 / 30)
		if months == 1 {
			return "1 month"
		}
		return fmt.Sprintf("%d months", months)
	default:
		years := (int(dur.Hours()) / 24 / 365)
		if years == 1 {
			return "1 year"
		}
		return fmt.Sprintf("%d years", years)
	}
}

func HandlePost(w http.ResponseWriter, req *http.Request) {
	tumblr := mux.Vars(req)["tumblr"]
	postId := mux.Vars(req)["postId"]
	slug := mux.Vars(req)["slug"]
	if slug != "" {
		slug = "/" + slug
	}
	resp, err := http.Get(fmt.Sprintf("https://%s.tumblr.com/post/%s%s", tumblr, postId, slug))
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: could not fetch post: %s", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	nightModeCSS := `* { color: #fff !important; background-color: #222 !important; }.tags { color: #b7b7b7 }a { color: pink; }a:visited { color: #a67070; }article{ border-bottom: 1px solid #666; }blockquote:not(:last-child) { border-bottom: 1px solid #333; }a.author,a.author:visited{color: #fff;}`
	modeCSS := `@media (prefers-color-scheme: dark) {` + nightModeCSS + `}`
	if _, ok := req.URL.Query()["night-mode"]; ok {
		modeCSS = nightModeCSS
	}
	fmt.Fprintf(w, `<!doctype html>
<html>
<head>

	<style>h1 { word-break: break-all; }blockquote, figure { margin: 0; }blockquote:not(:last-child) { border-bottom: 1px solid #ddd; } blockquote > blockquote:nth-child(1) { border-bottom: 0; }body { font-family: sans-serif; }article{ border-bottom: 1px solid black; padding: 1em 0; }.tags { list-style: none; padding: 0; color: #666; }.tags > li { display: inline }img, video, iframe { max-width: 95vw; }@media (min-width: 60em) { body { margin-left: 15vw; } article { max-width: 60em; } img, video { max-height: 20vh; width: auto; } img:hover, video:hover { max-height: 100%%; }}.avatar{height: 1em;}a.author,a.author:visited{color: #000;}%s</style>
</head>

<body>
`, modeCSS)

	node, err := html.Parse(resp.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: could not parse post: %s", err), http.StatusInternalServerError)
		return
	}

	var f func(*html.Node)
	f = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "a", "p", "img", "div", "span":
				if hasAttribute(node, "class", "app-nag") {
					return
				}

				err := html.Render(w, node)
				if err != nil {
					log.Printf("Error: rendering %q: %s", req.URL, err)
				}

				return
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(node)
}

func hasAttribute(node *html.Node, attrName, attrValue string) bool {
	for _, attr := range node.Attr {
		if attr.Key == attrName && attr.Val == attrValue {
			return true
		}
	}
	return false
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

func NewCachedFeed(name string) (Tumblr, error) {
	switch {
	case strings.HasSuffix(name, "@twitter"):
		return NewCachedTumblr(name, NewNitter)
	case strings.HasSuffix(name, "@instagram"):
		return NewCachedTumblr(name, NewBibliogram)
	case strings.Contains(name, "@") || strings.Contains(name, "."):
		return NewCachedTumblr(name, NewRSS)
	default:
		return NewCachedTumblr(name, NewTumblrRSS)
	}
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

	return &tumblrRSS{name: name, r: resp.Body, dec: dec, dateFormat: TumblrDate}, nil
}

type tumblrRSS struct {
	name       string
	r          io.ReadCloser
	dec        *xml.Decoder
	dateFormat string
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

	t, dateErr := time.Parse(tr.dateFormat, post.DateString)
	if dateErr != nil {
		return nil, fmt.Errorf("invalid date %q: %s", post.DateString, dateErr)
	}
	post.Date = t

	return &post, err
}

func (tr *tumblrRSS) Close() error {
	return tr.r.Close()
}
