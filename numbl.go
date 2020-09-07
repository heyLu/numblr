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
	"strconv"
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
	Source          string
	ID              string `xml:"guid"`
	Author          string
	AvatarURL       string
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
var blankLinksRE = regexp.MustCompile(` target="_blank"`)
var linkRE = regexp.MustCompile(`<a `)
var tumblrReblogLinkRE = regexp.MustCompile(`<a ([^>]*)href="(https?://[^.]+\.tumblr.com([^" ]+)?)"([^>]*)>([-\w]+)</a>\s*:`) // <a>account</a>:
var tumblrAccountLinkRE = regexp.MustCompile(`<a ([^>]*)href="[^"]+"([^>]*)>@([-\w]+)</a>`)                                   // @<account>
var tumblrLinksRE = regexp.MustCompile(`https?://([^.]+).tumblr.com([^" ]+)?`)
var videoRE = regexp.MustCompile(`<video `)
var autoplayRE = regexp.MustCompile(` autoplay="autoplay"`)

const CookieName = "numbl"

var config struct {
	Addr         string
	DatabasePath string

	DefaultTumblr string

	AppDisplayMode string
}

const CacheTime = 10 * time.Minute
const AvatarSize = 64
const AvatarCacheTime = 30 * 24 * time.Hour

var cacheFn CacheFn = NewCachedTumblr

var cache *lru.Cache
var avatarCache *lru.Cache

var httpClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func main() {
	flag.StringVar(&config.Addr, "addr", "localhost:5555", "Address to listen on")
	flag.StringVar(&config.DatabasePath, "db", "", "Database path to use")
	flag.StringVar(&config.DefaultTumblr, "default", "nettleforest", "Default tumblr to view")
	flag.StringVar(&config.AppDisplayMode, "app-display", "browser", "Display mode to use when installed as an app")
	flag.Parse()

	if config.DatabasePath != "" {
		db, err := InitDatabase(config.DatabasePath)
		if err != nil {
			log.Fatalf("setup database: %s", err)
		}

		cacheFn = func(name string, uncachedFn FeedFn) (Tumblr, error) {
			return NewDatabaseCached(db, name, uncachedFn)
		}

		go func() {
			refreshFn := func() {
				feeds, err := ListFeedsOlderThan(db, time.Now().Add(-CacheTime))
				if err != nil {
					log.Printf("Error: listing feeds in background: %s", err)
					return
				}

				for _, feedName := range feeds {
					func(feedName string) {
						feed, err := NewCachedFeed(feedName, cacheFn)
						if err != nil {
							log.Printf("Error: background refresh: opening feed: %s", err)
							return
						}
						defer func() {
							err := feed.Close()
							if err != nil {
								log.Printf("Error: background refresh: closing feed: %s", err)
							}
						}()

						_, err = feed.Next()
						for err == nil {
							_, err = feed.Next()
						}

						if err != nil && !errors.Is(err, io.EOF) {
							log.Printf("Error: background refresh: iterating feed: %s", err)
							return
						}
					}(feedName)
				}

				if len(feeds) > 0 {
					log.Printf("Refreshed %d feeds", len(feeds))
				}
			}

			for {
				go refreshFn()

				time.Sleep(1 * time.Minute)
			}
		}()
	}

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

		fmt.Fprintf(w, `{
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
  "display": %q,
  "orientation": "portrait",
  "background_color": "#222222",
  "theme_color": "#222222"
}`, config.AppDisplayMode)
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

		redirect := "/"
		cookieName := CookieName
		if list != "" {
			redirect = "/list/" + list
			cookieName = CookieName + "-list-" + list
		}

		if cookieValue == "" {
			http.Redirect(w, req, redirect, http.StatusTemporaryRedirect)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    cookieValue,
			MaxAge:   365 * 24 * 60 * 60, // one year
			SameSite: http.SameSiteLaxMode,
			HttpOnly: true,
		})
		http.Redirect(w, req, redirect, http.StatusSeeOther)
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

	router.HandleFunc("/", HandleTumblr)
	router.HandleFunc("/{tumblrs}", HandleTumblr)

	router.HandleFunc("/list/{list}", HandleTumblr)

	router.HandleFunc("/{tumblr}/post/{postId}", HandlePost)
	router.HandleFunc("/{tumblr}/post/{postId}/{slug}", HandlePost)

	router.HandleFunc("/avatar/{tumblr}", HandleAvatar)

	http.Handle("/", router)

	log.Printf("Listening on http://%s", config.Addr)
	log.Fatal(http.ListenAndServe(config.Addr, nil))
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

	if req.URL.Query().Get("feed") != "" {
		feed := req.URL.Query().Get("feed")
		if strings.ContainsAny(feed, "#?") {
			feed = url.PathEscape(feed)
		}
		http.Redirect(w, req, "/"+feed, http.StatusFound)
		return
	}

	tumbl := tumblsFromRequest(req)
	tumbls := strings.Split(tumbl, ",")

	search := parseSearch(req)

	limit := 25
	limitParam := req.URL.Query().Get("limit")
	if limitParam != "" {
		l, err := strconv.Atoi(limitParam)
		if err != nil {
			log.Printf("Error: parsing limit: %s", err)
		} else {
			limit = l
		}
	}

	w.Header().Set("Content-Type", `text/html; charset="utf-8"`)

	nightModeCSS := `body { color: #fff; background-color: #222; }.tags { color: #b7b7b7 }a { color: pink; }a:visited { color: #a67070; }article{ border-bottom: 1px solid #666; }blockquote:not(:last-child) { border-bottom: 1px solid #333; }a.author,a.author:visited{color: #fff;}`
	modeCSS := `@media (prefers-color-scheme: dark) {` + nightModeCSS + `}`
	if _, ok := req.URL.Query()["night-mode"]; ok {
		modeCSS = nightModeCSS
	}
	title := tumbl
	if req.URL.Path == "" || req.URL.Path == "/" {
		title = "everything"
	} else if mux.Vars(req)["list"] != "" {
		title = mux.Vars(req)["list"]
	}
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width,minimum-scale=1,initial-scale=1" />
	<meta name="description" content="Mirror of %s tumblrs" />
	<title>%s</title>
	<style>.jumper { font-size: 2em; float: right; text-decoration: none; }.jump-to-top { position: sticky; bottom: 0.25em; }h1 { word-break: break-all; }blockquote, figure { margin: 0; }blockquote:not(:last-child) { border-bottom: 1px solid #ddd; } blockquote > blockquote:nth-child(1) { border-bottom: 0; }body { font-family: sans-serif; }article{ border-bottom: 1px solid black; padding: 1em 0; }.tags { list-style: none; padding: 0; color: #666; }.tags > li { display: inline }img, video, iframe { max-width: 95vw; }@media (min-width: 60em) { body { margin-left: 15vw; max-width: 60em; } img, video { max-height: 20vh; } img:hover, video:hover { max-height: 100%%; }}.avatar{height: 1em;vertical-align: middle;}a.author,a.author:visited{color: #000; font-weight: bold;}.next-page { display: flex; justify-content: center; padding: 1em; }%s</style>
	<link rel="preconnect" href="https://64.media.tumblr.com/" />
	<link rel="manifest" href="/manifest.webmanifest" />
	<meta name="theme_color" content="#222222" />
	<link rel="icon" href="/favicon.png" />
</head>

<body>

<a id="top" class="jumper" href="#bottom">▾</a>

<h1>%s</h1>

`, tumbl, title, modeCSS, title)

	fmt.Fprintf(w, `<form method="GET" action=%q><input aria-label="visit feed" name="feed" type="search" value="" placeholder="feed" /></form>`, req.URL.Path)
	fmt.Fprintf(w, `<form method="GET" action=%q><input aria-label="search posts" name="search" type="search" value=%q placeholder="noreblog #art ..." /></form>`, req.URL.Path, req.URL.Query().Get("search"))

	var tumblr Tumblr
	var err error
	if strings.Contains(tumbl, ",") {
		tumblrs := make([]Tumblr, len(tumbls))
		var wg sync.WaitGroup
		wg.Add(len(tumbls))
		for i := range tumbls {
			go func(i int) {
				var openErr error
				tumblrs[i], openErr = NewCachedFeed(tumbls[i], cacheFn)
				if openErr != nil {
					err = fmt.Errorf("%s: %w", tumbls[i], openErr)
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
		tumblr, err = NewCachedFeed(tumbl, cacheFn)
	}
	if err != nil {
		log.Println("open:", err)
		fmt.Fprintf(w, `<code style="color: red; font-weight: bold; font-size: larger;">could not load tumblr: %s</code>`, err)
		if tumblr == nil {
			return
		}
	}
	defer func() {
		err := tumblr.Close()
		if err != nil {
			log.Printf("Error: closing %s: %s", tumbl, err)
		}
	}()
	openTime := time.Since(start)

	postCount := 0
	var post *Post
	var lastPost *Post
	nextPost := func() {
		lastPost = post
		post, err = tumblr.Next()
	}

	beforeParam := req.URL.Query().Get("before")
	if beforeParam != "" {
		nextPost()
		for err == nil {
			if post.ID == beforeParam {
				break
			}
			nextPost()
		}
	}

	imageCount := 0
	nextPost()
	for err == nil {
		if !search.Matches(post) {
			nextPost()
			continue
		}

		classes := make([]string, 0, 1)
		if post.IsReblog() {
			classes = append(classes, "reblog")
		}

		if postCount >= limit {
			break
		}
		postCount++

		fmt.Fprintf(w, `<article class=%q>`, strings.Join(classes, " "))
		avatarURL := post.AvatarURL
		if avatarURL == "" {
			avatarURL = "/avatar/" + post.Author
		}
		fmt.Fprintf(w, `<p><img class="avatar" src="%s" /> <a class="author" href="/%s">%s</a>:<p>`, avatarURL, post.Author, post.Author)

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
		postHTML = blankLinksRE.ReplaceAllString(postHTML, ` `)
		postHTML = linkRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			return `<a rel="noreferrer" `
		})
		postHTML = tumblrReblogLinkRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			parts := tumblrReblogLinkRE.FindStringSubmatch(repl)
			if len(parts) != 6 {
				log.Printf("Error: invalid tumblr reblog link: %s", repl)
				return repl
			}

			u, err := url.Parse(parts[2])
			if err != nil {
				log.Printf("could not parse url: %s", err)
				return repl
			}

			tumblrName := u.Host[:strings.Index(u.Host, ".")]
			u.Host = ""
			u.Scheme = ""
			u.Path = path.Join("/", tumblrName, u.Path)

			reblogLink := u.String()
			tumblrLink := "/" + tumblrName

			return fmt.Sprintf(`<img class="avatar" src=%q /> <a href=%q>%s</a> (<a %shref=%q%s>post</a>):`, "/avatar/"+tumblrName, tumblrLink, tumblrName, parts[1], reblogLink, parts[4])
		})
		postHTML = tumblrAccountLinkRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
			parts := tumblrAccountLinkRE.FindStringSubmatch(repl)
			if len(parts) != 4 {
				log.Printf("Error: invalid tumblr account link: %s", repl)
				return repl
			}

			return fmt.Sprintf(`<a %shref=%q%s>%s</a>`, parts[1], "/"+parts[3], parts[2], "@"+parts[3])
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

	fmt.Fprintln(w, `<span id="bottom"></span>
<a id="link-top" class="jumper" href="#top">▴</a>`)

	if err == nil && lastPost != nil {
		url := req.URL
		query := url.Query()
		query.Set("before", lastPost.ID)
		url.RawQuery = query.Encode()
		fmt.Fprintf(w, `<div class="next-page"><a href="%s">next page</a></div>`, url)
	}

	fmt.Fprintf(w, `<form method="POST" action="/settings">

	<input type="text" name="list" hidden value=%q />

	<label for="tumblrs">Tumblrs to view by default</label>:
	<div class="field">
		<textarea rows="%d" cols="30" name="tumblrs">%s</textarea>
	</div>
	<input type="submit" value="Save" />
</form>

<form method="POST" action="/settings/clear">
	<input type="submit" value="Clear" title="FIXME: clear currently broken :/" disabled />
</form>
`, mux.Vars(req)["list"], len(tumbls)+1, strings.Join(tumbls, "\n"))

	fmt.Fprintln(w, `<section id="lists">
<h1>Lists</h1>

<ul>

<li><a href="/">everything</a></li>`)

	for _, cookie := range req.Cookies() {
		if strings.HasPrefix(cookie.Name, CookieName+"-list-") {
			listName := cookie.Name[len(CookieName+"-list-"):]
			fmt.Fprintf(w, `<li><a href="/list/%s">%s</a></li>`, listName, listName)
		}
	}
	fmt.Fprintln(w, `</ul>
</section>`)

	fmt.Fprintf(w, `<hr /><footer>%d posts from %q (<a href=%q>source</a>) in %s (open: %s)</footer>`, postCount, tumblr.Name(), tumblr.URL(), time.Since(start).Round(time.Millisecond), openTime.Round(time.Millisecond))
	if err != nil && !errors.Is(err, io.EOF) {
		log.Println("decode:", err)
	}

	fmt.Fprintln(w, `<script>
  let lastScrollTop = window.pageYOffset || document.documentElement.scrollTop;;

  var toTopEl = document.querySelector("#link-top");
  window.addEventListener("scroll", function() {
    let st = window.pageYOffset || document.documentElement.scrollTop;
    if (st > lastScrollTop){
      toTopEl.classList.remove("jump-to-top");
    } else {
      toTopEl.classList.add("jump-to-top");
    }
    lastScrollTop = st <= 0 ? 0 : st;
  }, false);

  function reloadSpinner() {
    let reloadStyleEl = document.createElement("style");
    reloadStyleEl.textContent = "#reload { position: fixed; top: 1ex; left: 50vw; animation: reload 3s infinite; } @keyframes reload { 0% { color: black; } 12.5% { color: violet; } 25% { color: blue; } 37.5% { color: green; } 50% { color: yellow; } 62.5% { color: orange; } 75% { color: red; } 87.5% { color: brown; } 100% { color: black; } }";
    document.body.appendChild(reloadStyleEl);
    let reloadEl = document.createElement("div");
    reloadEl.id = "reload";
    reloadEl.textContent = "✦";
    document.body.appendChild(reloadEl);
  };

  window.addEventListener("beforeunload", reloadSpinner);

  let startY;

  window.addEventListener('touchstart', e => {
    startY = e.touches[0].pageY;
  }, {passive: true});

  window.addEventListener('touchmove', e => {
    const y = e.touches[0].pageY;
    if (document.scrollingElement.scrollTop === 0 && y > startY) {
      let url = new URL(window.location);
      if (url.searchParams.has("before")) {
        url.searchParams.delete("before");
      }
      url.hash = "";
      window.location = url.href;
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

type Search struct {
	IsActive bool

	NoReblogs    bool
	Terms        []string
	Tags         []string
	ExcludeTerms []string
	ExcludeTags  []string
}

func (s *Search) Matches(p *Post) bool {
	if !s.IsActive {
		return true
	}

	if s.NoReblogs && p.IsReblog() {
		return false
	}

	for _, tag := range p.Tags {
		for _, exclude := range s.ExcludeTags {
			if tag == exclude {
				return false
			}
		}
	}

	// must match all tags
	for _, tag := range s.Tags {
		if !contains(p.Tags, tag) {
			return false
		}
	}

	for _, term := range s.Terms {
		if !strings.Contains(strings.ToLower(p.Title), term) && !strings.Contains(strings.ToLower(p.DescriptionHTML), term) {
			return false
		}
	}

	for _, term := range s.ExcludeTerms {
		if strings.Contains(strings.ToLower(p.Title), term) || strings.Contains(strings.ToLower(p.DescriptionHTML), term) {
			return false
		}
	}

	return true
}

func contains(xs []string, contain string) bool {
	for _, x := range xs {
		if strings.ToLower(x) == contain {
			return true
		}
	}
	return false
}

func parseSearch(req *http.Request) Search {
	searchTerms := strings.Fields(req.URL.Query().Get("search"))
	if len(searchTerms) == 0 {
		return Search{}
	}

	search := Search{
		IsActive:     true,
		Terms:        make([]string, 0, 1),
		Tags:         make([]string, 0, 1),
		ExcludeTags:  make([]string, 0, 1),
		ExcludeTerms: make([]string, 0, 1),
	}

	for _, searchTerm := range searchTerms {
		if searchTerm == "noreblog" {
			search.NoReblogs = true
			continue
		}

		exclude := false
		if searchTerm[0] == '-' {
			exclude = true
			searchTerm = searchTerm[1:]
		}

		tag := false
		if searchTerm[0] == '#' {
			tag = true
			searchTerm = searchTerm[1:]
		}

		unescaped, err := url.QueryUnescape(searchTerm)
		if err == nil {
			searchTerm = unescaped
		}

		searchTerm = strings.ToLower(searchTerm)

		switch {
		case exclude && tag:
			search.ExcludeTags = append(search.ExcludeTags, searchTerm)
		case tag:
			search.Tags = append(search.Tags, searchTerm)
		case exclude:
			search.ExcludeTerms = append(search.ExcludeTerms, searchTerm)
		default:
			search.Terms = append(search.Terms, searchTerm)
		}
	}

	return search
}

func tumblsFromRequest(req *http.Request) string {
	isList := strings.HasPrefix(req.URL.Path, "/list/")

	// explicitely specified
	tumbl := req.URL.Path[1:]
	if tumbl != "" && !isList {
		return tumbl
	}

	cookieName := CookieName
	if isList {
		cookieName = CookieName + "-list-" + mux.Vars(req)["list"]
	}

	cookie, err := req.Cookie(cookieName)
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

type FeedFn func(name string) (Tumblr, error)
type CacheFn func(name string, uncachedFn FeedFn) (Tumblr, error)

func NewCachedFeed(name string, cacheFn CacheFn) (Tumblr, error) {
	switch {
	case strings.HasSuffix(name, "@twitter"):
		return cacheFn(name, NewNitter)
	case strings.HasSuffix(name, "@instagram"):
		return cacheFn(name, NewBibliogram)
	case strings.Contains(name, "@") || strings.Contains(name, "."):
		return cacheFn(name, NewRSS)
	default:
		return cacheFn(name, NewTumblrRSS)
	}
}

func NewCachedTumblr(name string, uncachedFn FeedFn) (Tumblr, error) {
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

var tumblrPostURLRE = regexp.MustCompile(`https?://([-\w]+).tumblr.com/post/(\d+)(/(.*))?`)

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

	return &post, err
}

func (tr *tumblrRSS) Close() error {
	return tr.r.Close()
}
