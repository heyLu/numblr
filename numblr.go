package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/gorilla/mux"
	lru "github.com/hashicorp/golang-lru"
	"github.com/yuin/goldmark"
	"golang.org/x/net/html"

	"github.com/heyLu/numblr/feed"
	"github.com/heyLu/numblr/feed/anything"
	"github.com/heyLu/numblr/feed/bibliogram"
	"github.com/heyLu/numblr/feed/database"
	"github.com/heyLu/numblr/feed/nitter"
	"github.com/heyLu/numblr/feed/tumblr"
)

var contentNoteRE = regexp.MustCompile(`\b(tw|trigger warning|cn|content note|cw|content warning)\b`)
var imgRE = regexp.MustCompile(`<img `)
var widthHeightRE = regexp.MustCompile(` (width|height|style)="[^"]+"`)
var origWidthHeightRE = regexp.MustCompile(`data-orig-width="(\d+)" data-orig-height="(\d+)"`)
var origHeightWidthRE = regexp.MustCompile(`data-orig-height="(\d+)" data-orig-width="(\d+)"`)
var blankLinksRE = regexp.MustCompile(` target="_blank"`)
var linkRE = regexp.MustCompile(`<a `)
var tumblrReblogLinkRE = regexp.MustCompile(`<a ([^>]*)href="(https?://[^.]+\.tumblr.com([^" ]+)?)"([^>]*)>([-\w]+)</a>\s*:`) // <a>account</a>:
var tumblrAccountLinkRE = regexp.MustCompile(`<a ([^>]*)href="[^"]+"([^>]*)>@([-\w]+)</a>`)                                   // @<account>
var tumblrLinksRE = regexp.MustCompile(`https?://([^.]+).t?umblr.com([^" ]+)?`)
var instagramLinksRE = regexp.MustCompile(`https?://(www\.)?instagram.com/([^/" ]+)[^" ]*`)
var altTextRE = regexp.MustCompile(`alt="([^"]+)"|alt='([^']+)'`)
var videoRE = regexp.MustCompile(`<video `)
var autoplayRE = regexp.MustCompile(` autoplay="autoplay"`)

const CookieName = "numbl"
const UserAgent = "numblr"

var config struct {
	Addr         string
	DatabasePath string
	DebugAddr    string

	DefaultFeed string

	AppDisplayMode string

	CollectStats bool
}

const CacheTime = 10 * time.Minute
const AvatarSize = 32
const AvatarCacheTime = 30 * 24 * time.Hour

const GroupPostsNumber = 5
const TagsCollapseCount = 20

//go:embed favicon.png
var FaviconPNGBytes []byte

//go:embed README.md
var ReadmeBytes []byte

//go:embed CHANGELOG.md
var ChangelogBytes []byte

//go:embed help.md
var HelpBytes []byte

var cacheFn feed.OpenCached = nil

var avatarCache *lru.Cache

var httpClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type userAgentTransport struct {
	UserAgent string
	Transport http.RoundTripper
}

func (uat *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", uat.UserAgent)
	return uat.Transport.RoundTrip(req)
}

func main() {
	flag.StringVar(&config.Addr, "addr", "localhost:5555", "Address to listen on")
	flag.StringVar(&config.DatabasePath, "db", "", "Database path to use")
	flag.StringVar(&config.DebugAddr, "debug-addr", "", "Address to listen on for debug interface (disable by default)")
	flag.StringVar(&config.DefaultFeed, "default", "staff,engineering", "Default feeds to view")
	flag.StringVar(&config.AppDisplayMode, "app-display", "browser", "Display mode to use when installed as an app")
	flag.BoolVar(&config.CollectStats, "stats", false, "Whether to collect anonymized stats (num cached feeds & posts, recent errors & user agents")
	flag.StringVar(&nitter.NitterURL, "nitter-url", "https://nitter.net", "Nitter instance to use")
	flag.StringVar(&bibliogram.BibliogramInstancesURL, "bibliogram-instances-url", bibliogram.BibliogramInstancesURL, "The bibliogram url to use to fetch possible instances from")
	flag.Parse()

	http.DefaultClient.Timeout = 10 * time.Second
	http.DefaultClient.Transport = &userAgentTransport{
		UserAgent: UserAgent,
		Transport: http.DefaultTransport,
	}

	if config.CollectStats {
		EnableStats(20, 20, 20)

		log.SetOutput(io.MultiWriter(os.Stdout, &CollectLogsWriter{}))
	}

	db, err := database.InitDatabase(config.DatabasePath)
	if err != nil {
		log.Fatalf("setup database: %s", err)
	}

	cacheFn = func(ctx context.Context, name string, uncachedFn feed.Open, search feed.Search) (feed.Feed, error) {
		return database.OpenCached(ctx, db, name, uncachedFn, search)
	}

	if config.CollectStats {
		EnableDatabaseStats(db, config.DatabasePath)
	}

	go func() {
		maxConcurrentFeeds := make(chan bool, 100)

		refreshFn := func() {
			feeds, err := database.ListFeedsOlderThan(context.Background(), db, time.Now().Add(-CacheTime))
			if err != nil {
				log.Printf("Error: listing feeds in background: %s", err)
				return
			}

			successfulFeeds := 0
			for _, feedName := range feeds {
				err := func(feedName string) error {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()

					feed, err := anything.Open(ctx, feedName, cacheFn, feed.Search{ForceFresh: true})
					if err != nil {
						return fmt.Errorf("background refresh: opening %s: %s", feedName, err)
					}
					maxConcurrentFeeds <- true
					defer func() {
						err := feed.Close()
						if err != nil {
							err = fmt.Errorf("background refresh: closing %s: %s", feedName, err)
							CollectError(err)
							log.Printf("Error: %s", err)
						}
						<-maxConcurrentFeeds
					}()

					_, err = feed.Next()
					for err == nil {
						_, err = feed.Next()
					}

					if err != nil && !errors.Is(err, io.EOF) {
						return fmt.Errorf("background refresh: iterating %s: %s", feedName, err)
					}

					successfulFeeds++
					return nil
				}(feedName)
				if err != nil {
					CollectError(err)
					log.Printf("Error: %s", err)
				}
			}

			if len(feeds) > 0 {
				log.Printf("Refreshed %d/%d feeds", successfulFeeds, len(feeds))
			}
		}

		for {
			go refreshFn()

			time.Sleep(1 * time.Minute)
		}
	}()

	avatarCache, err = lru.New(100)
	if err != nil {
		log.Fatal("setup avatar cache:", err)
	}

	router := mux.NewRouter()
	router.Use(gziphandler.GzipHandler)
	router.Use(strictTransportSecurity)

	router.Handle("/stats", http.HandlerFunc(StatsHandler))

	router.HandleFunc("/favicon.ico", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/favicon.png", http.StatusPermanentRedirect)
	})
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
  "description": "Alternative Tumblr (and Twitter, Instagram, AO3, RSS, ...) frontend.",
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

	router.HandleFunc("/about", func(w http.ResponseWriter, req *http.Request) {
		htmlPrelude(w, req, "about:numblr", "about:numblr", "/favicon.png")

		err := goldmark.Convert(ReadmeBytes, w)
		if err != nil {
			log.Printf("Could not render about page: %s", err)

		}
	})

	router.HandleFunc("/changes", func(w http.ResponseWriter, req *http.Request) {
		htmlPrelude(w, req, "changes", "changes", "/favicon.png")

		err := goldmark.Convert(ChangelogBytes, w)
		if err != nil {
			log.Printf("Could not render changes page: %s", err)

		}
	})

	router.HandleFunc("/help.md", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/hjälp", http.StatusTemporaryRedirect)
	})

	router.HandleFunc("/hjälp", func(w http.ResponseWriter, req *http.Request) {
		htmlPrelude(w, req, "hjälp", "hjälp", "/favicon.png")

		err := goldmark.Convert(HelpBytes, w)
		if err != nil {
			log.Printf("Could not render hjälp page: %s", err)

		}
	})

	// TODO: implement a follow button (first only for generic feed, either add to cookie or url, depending on context)

	router.HandleFunc("/settings", func(w http.ResponseWriter, req *http.Request) {
		list := req.FormValue("list")
		feeds := req.FormValue("feeds")

		first := true
		cookieValue := ""
		for _, feed := range strings.Split(feeds, "\n") {
			feed = strings.TrimSpace(feed)
			if feed == "" {
				continue
			}
			if !first {
				cookieValue += ","
			}
			first = false
			cookieValue += feed
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

	router.HandleFunc("/proxy", func(w http.ResponseWriter, req *http.Request) {
		proxyURL := req.URL.Query().Get("url")
		if !strings.Contains(proxyURL, ".tiktok.com/") && !strings.Contains(proxyURL, "media_type=video_") {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}

		req, err := http.NewRequest("GET", proxyURL, nil)
		if err != nil {
			log.Printf("Error: new request: %s", err)
			return
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("Error: proxy %q: %s", req.URL, err)
			return
		}
		defer resp.Body.Close()

		io.Copy(w, resp.Body)
	})

	router.HandleFunc("/", HandleTumblr)
	router.HandleFunc("/{feeds}", HandleTumblr)
	router.HandleFunc("/{feeds}/", HandleTumblr)
	router.HandleFunc("/{feeds}/tagged/{tag}", HandleTumblr)

	router.HandleFunc("/list/{list}", HandleTumblr)

	router.HandleFunc("/{tumblr}/post/{postId}", HandlePost)
	router.HandleFunc("/{tumblr}/post/{postId}/{slug}", HandlePost)

	router.HandleFunc("/avatar/{tumblr}", HandleAvatar)

	if config.DebugAddr != "" {
		go func() {
			debug := http.NewServeMux()
			debug.HandleFunc("/debug/pprof/", pprof.Index)
			log.Printf("Debug interface listening on on http://%s", config.DebugAddr)
			log.Fatal(http.ListenAndServe(config.DebugAddr, nil))
		}()
	}

	log.Printf("Listening on http://%s", config.Addr)
	log.Fatal(http.ListenAndServe(config.Addr, router))
}

func htmlPrelude(w http.ResponseWriter, req *http.Request, title, description, favicon string) {
	w.Header().Set("Content-Type", `text/html; charset="utf-8"`)

	nightModeCSS := `body { --text-color: #fff; color: #fff; background-color: #222; }.tags a,.tags a:visited{ color: #b7b7b7; text-decoration: none;}a { color: pink; }a:visited { color: #a67070; }article,details:not([open]){ border-bottom: 1px solid #666; }blockquote:not(:last-child) { border-bottom: 1px solid #333; }a.author,a.author:visited,a.tumblr-link,a.tumblr-link:visited{color: #fff;}img{filter: brightness(.8) contrast(1.2);} #menu a { color: #fff; }`
	modeCSS := `@media (prefers-color-scheme: dark) {` + nightModeCSS + `}`
	if _, ok := req.URL.Query()["night-mode"]; ok {
		modeCSS = nightModeCSS
	}

	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width,minimum-scale=1,initial-scale=1" />
	<meta name="color-scheme" content="dark light" />
	<meta name="description" content="%s" />
	<title>%s</title>
	<style>body { --text-color: #000; margin: 0; } #menu { --blue: 0, 0, 255; background: linear-gradient(to right, rgba(var(--blue), 0.1), pink); font-family: monospace; font-size: large; font-weight: bold; } #menu ul { display: flex; list-style-type: none; padding-left: 0; padding: 0.3em; margin: 0 auto; max-width: 69em; } #menu ul li { padding-left: 0.75em; } #menu ul li:first-of-type { padding-left: 0; flex-grow: 4; }</style>
	<style>header { margin-bottom: 1em; } header h1 { margin-bottom: 0; } header h2 { margin: auto 0; font-size: initial; font-weight: normal; }.jumper { font-size: 2em; float: right; text-decoration: none; }.jump-to-top { position: sticky; bottom: 0.25em; }blockquote, figure { margin: 0; }blockquote:not(:last-child) { border-bottom: 1px solid #ddd; } blockquote.question{padding-left: 2em;}blockquote.question ::before, blockquote.question ::after { content: "“"; font-family: serif; font-size: x-large; }#content { scroll-behavior: smooth; font-family: sans-serif; overflow-wrap: break-word; margin: 8px; }article,details:not([open]){ border-bottom: 1px solid black; padding-bottom: 1em; margin-bottom: 1em; }article h1 a, article h4 a { text-decoration: none; border-bottom: 1px dotted black; }section.hidden { opacity: 0.5; }.tags { list-style: none; padding: 0; color: #666; }.tags li, .tags display, tags display[open] { display: inline }.tags a, .tags a:visited{color: #333; text-decoration: none;}img:not(.avatar), video, iframe { max-width: 100%%; height: auto; object-fit: contain } video::cue { font-size: 1rem; } @media (min-width: 60em) { #content { margin: 0 auto; max-width: 60em; } img:not(.avatar), video { max-height: 50vh; width: auto; object-fit: contain; } img:hover:not(.avatar)}.avatar,img[class*="avatar"],img[src*="static.tumblr.com"],img[src*="avatar"]{width: 1em;height: 1em;vertical-align: middle;display:inline-block;}a.author,a.author:visited,a.tumblr-link,a.tumblr-link:visited{color: #000; font-weight: bold;}a.tumblr-link{padding: 0.5em; text-decoration: none; font-size: larger; vertical-align: middle;}.next-page { display: flex; justify-content: center; padding: 1em; }.ao3 dl dt, .ao3 dl dd { display: inline; margin-left: 0}.ao3 blockquote { border: none; }textarea{ width: 100%%; }.tiktok .tag { color: var(--text-color); }%s</style>
	<link rel="preconnect" href="https://64.media.tumblr.com/" />
	<link rel="manifest" href="/manifest.webmanifest" />
	<meta name="theme-color" content="#222222" />
	<link rel="icon" href="%s" />
</head>

<body>

<nav id="menu">
	<ul>
		<li><a href="/" title="Alternative Tumblr (and Twitter, Instagram, AO3, RSS, ...) frontend."><img style="height: 1em; vertical-align: sub;" src="/favicon.png" /> numblr</a></li>

		<li><a href="/about" title="wtf is this?!">/about</a></li>
		<li><a href="/changes">/changes</a></li>
		<li lang="sv"><a href="/hjälp">/hjälp</a></li>
		<li><a href="https://github.com/heyLu/numblr">/source</a></li>
	</ul>
</nav>

<div id="content">
`, description, title, modeCSS, favicon)
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

	req, err := http.NewRequestWithContext(req.Context(), "GET", avatarURL, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: fetching avatar: could not create request: %s", err), http.StatusInternalServerError)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: fetching avatar: %s", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		http.Error(w, fmt.Sprintf("Error: fetching avatar: %s", feed.StatusError{Code: resp.StatusCode}), http.StatusInternalServerError)
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

func strictTransportSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Strict-Transport-Security", fmt.Sprintf("max-age=%d", 365*24*60*60))
		next.ServeHTTP(w, req)
	})
}

type FeedInfo struct {
	Duration time.Duration
	Error    error
	Feed     feed.Feed
}

func HandleTumblr(w http.ResponseWriter, req *http.Request) {
	start := time.Now()

	go CountView()
	go CollectUser(req.Header.Get("User-Agent"))

	if req.URL.Query().Get("feed") != "" {
		feed := req.URL.Query().Get("feed")
		if strings.ContainsAny(feed, "#?") {
			feed = url.PathEscape(feed)
		}
		http.Redirect(w, req, "/"+feed, http.StatusFound)
		return
	}

	if len(req.URL.Path) > 1 && strings.HasSuffix(req.URL.Path, "/") {
		http.Redirect(w, req, req.URL.Path[:len(req.URL.Path)-1], http.StatusFound)
		return
	}

	tag := mux.Vars(req)["tag"]
	if tag != "" {
		req.URL.Path = strings.Replace(req.URL.Path, "/tagged/"+tag, "", 1)
	}

	settings := SettingsFromRequest(req)
	search := feed.FromRequest(req)

	if tag != "" {
		search.IsActive = true
		search.Tags = append(search.Tags, strings.ToLower(tag))
	}

	var mergedFeeds feed.Feed
	var err error
	var feedInfoMu sync.Mutex
	feedInfo := make(map[string]FeedInfo, len(settings.SelectedFeeds))
	feeds := make([]feed.Feed, len(settings.SelectedFeeds))
	var wg sync.WaitGroup
	wg.Add(len(settings.SelectedFeeds))
	for i := range settings.SelectedFeeds {
		go func(i int) {
			var openErr error
			defer func() {
				wg.Done()
				feedInfoMu.Lock()
				feedName := settings.SelectedFeeds[i]
				if feeds[i] != nil {
					feedName = feeds[i].Name()
				}
				feedInfo[feedName] = FeedInfo{
					Duration: time.Since(start),
					Error:    openErr,
					Feed:     feeds[i],
				}
				feedInfoMu.Unlock()
			}()

			if strings.HasPrefix(settings.SelectedFeeds[i], ":") {
				return
			}

			feeds[i], openErr = anything.Open(req.Context(), settings.SelectedFeeds[i], cacheFn, search)
			if openErr != nil {
				err = fmt.Errorf("%s: %w", settings.SelectedFeeds[i], openErr)
			}
		}(i)
	}

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

	title := strings.Join(settings.SelectedFeeds, ",")
	if req.URL.Path == "" || req.URL.Path == "/" {
		title = "everything"
	} else if mux.Vars(req)["list"] != "" {
		title = mux.Vars(req)["list"]
	}

	favicon := "/favicon.png"
	if len(settings.SelectedFeeds) == 1 {
		favicon = "/avatar/" + url.PathEscape(settings.SelectedFeeds[0])
	}
	htmlPrelude(w, req, title, "Mirror of "+title+" feeds", favicon)

	fmt.Fprintf(w, `<a class="jumper" href="#bottom">▾</a>

<header>
	<h1>%s</h1>

`, title)

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	wg.Wait()
	successfulFeeds := make([]feed.Feed, 0, len(feeds))
	for _, feed := range feeds {
		if feed == nil {
			continue
		}
		successfulFeeds = append(successfulFeeds, feed)
	}
	mergedFeeds = feed.Merge(successfulFeeds...)
	if err != nil {
		go CollectError(err)
		log.Println("open:", err)
		numErrors := 0
		for _, info := range feedInfo {
			if info.Error != nil {
				numErrors++
			}
		}
		if numErrors > 1 {
			err = fmt.Errorf("%w (and %d more)", err, numErrors-1)
		}
		fmt.Fprintf(w, `<code style="color: red; font-weight: bold; font-size: larger;">could not load feed: %s</code>`, err)
		if mergedFeeds == nil {
			return
		}
	}
	defer func() {
		err := mergedFeeds.Close()
		if err != nil {
			log.Printf("Error: closing %s: %s", settings.SelectedFeeds, err)
		}
	}()
	openTime := time.Since(start)

	if len(settings.SelectedFeeds) == 1 && feeds[0] != nil && feeds[0].Description() != "" {
		fmt.Fprintf(w, "<h2 id=\"description\">%s</h2>\n", feeds[0].Description())
	}
	fmt.Fprintln(w, "</header>")

	fmt.Fprintf(w, `<form method="GET" action=%q><input aria-label="visit feed" name="feed" type="search" value="" placeholder="feed" list="feeds" /></form>`, req.URL.Path)
	fmt.Fprintln(w, `<datalist id="feeds">`)
	for _, tumbl := range settings.SelectedFeeds {
		fmt.Fprintf(w, `<option value=%q>%s</option>`, tumbl, tumbl)
	}
	fmt.Fprintln(w, `</datalist>`)
	fmt.Fprintf(w, `<form method="GET" action=%q><input aria-label="search posts" name="search" type="search" value=%q placeholder="noreblog #art ..." /></form>`, req.URL.Path, html.EscapeString(req.URL.Query().Get("search")))

	postCount := 0
	var post *feed.Post
	var lastPost *feed.Post
	var nextPost func()
	nextPost = func() {
		lastPost = post

		start := time.Now()
		post, err = mergedFeeds.Next()
		dur := time.Since(start)

		if dur > 200*time.Millisecond {
			feedName := "unknown"
			if post != nil {
				feedName = post.Author
			}
			log.Printf("slow next element for feed %q (%#v): %s", feedName, search, dur)
			info := feedInfo[feedName]
			info.Duration += dur
			feedInfo[feedName] = info
		}

		if post == nil || err != nil {
			return
		}

		if settings.GlobalSearch.Skip && !settings.GlobalSearch.Matches(post) {
			nextPost()
			return
		}

		if filter, hasFilter := settings.Searches[post.Author]; hasFilter && filter.Skip && !filter.Matches(post) {
			nextPost()
		}
	}

	if search.BeforeID != "" {
		nextPost()
		for err == nil {
			if post.ID <= search.BeforeID {
				break
			}
			nextPost()
		}
	}

	posts := make([]*feed.Post, 0, limit)

	nextPost()
	for err == nil {
		if !search.Matches(post) {
			nextPost()
			continue
		}

		if postCount >= limit {
			break
		}

		postCount++

		posts = append(posts, post)

		nextPost()
	}

	postGroups := make([][]*feed.Post, 0, limit)

	group, rest := nextPostsGroup(posts, GroupPostsNumber)
	for len(rest) != 0 {
		postGroups = append(postGroups, group)

		group, rest = nextPostsGroup(rest, GroupPostsNumber)
	}
	if len(group) > 0 {
		postGroups = append(postGroups, group)
	}

	imageCount := 0
	for _, group := range postGroups {
		if len(settings.SelectedFeeds) > 1 && len(group) >= GroupPostsNumber {
			fmt.Fprintf(w, `<details open><summary>%d posts by %s</summary>`, len(group), group[0].Author)
		}

		for _, post := range group {
			classes := make([]string, 0, 1)
			if post.IsReblog() {
				classes = append(classes, "reblog")
			}

			classes = append(classes, post.Source)

			isHidden := false
			postFilter, hasFilter := settings.Searches[post.Author]
			if !settings.GlobalSearch.Matches(post) {
				isHidden = true
				classes = append(classes, "hidden")
				postFilter = settings.GlobalSearch
			} else if hasFilter && !postFilter.Matches(post) {
				isHidden = true
				classes = append(classes, "hidden")
			}

			fmt.Fprintf(w, `<article class=%q>`, strings.Join(classes, " "))
			avatarURL := post.AvatarURL
			if avatarURL == "" {
				avatarURL = "/avatar/" + post.Author
			}
			feedDescription := ""
			if feedInfo[post.Author].Feed != nil {
				feedDescription = feedInfo[post.Author].Feed.Description()
			}
			fmt.Fprintf(w, `<p><img class="avatar" src="%s" loading="lazy" /> <a class="author" title=%q href="/%s">%s</a>:</p>`, avatarURL, html.EscapeString(feedDescription), post.Author, post.Author)

			if len(post.Tags) > 0 {
				fmt.Fprint(w, `<ul class="tags content-notes">`)
				for _, tag := range post.Tags {
					if contentNoteRE.MatchString(tag) {
						fmt.Fprintf(w, `<li>#%s</li> `, tag)
					}
				}
				fmt.Fprintln(w, `</ul>`)
			}

			fmt.Fprintf(w, `<section class="post-content %s">`, strings.Join(classes, " "))
			fmt.Fprintln(w)

			postHTML := ""
			if post.Title != "Photo" && !post.IsReblog() {
				postHTML = html.UnescapeString(post.Title)
			}
			if post.Source == "tumblr" && post.IsReblog() {
				reblogHTML, err := tumblr.FlattenReblogs(post.DescriptionHTML)
				if err != nil {
					log.Printf("Error: flatten reblog: %s", err)
				}
				postHTML = reblogHTML
			} else {
				postHTML += post.DescriptionHTML
			}
			postHTML = strings.ReplaceAll(postHTML, "<body>", "")
			postHTML = strings.ReplaceAll(postHTML, "</body>", "")
			// load first 5 images eagerly, and the rest lazily
			postHTML = imgRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				imageCount++
				if imageCount > 0 {
					return `<img loading="lazy" `
				}
				return `<img `
			})
			//postHTML = widthHeightRE.ReplaceAllString(postHTML, ` `)
			postHTML = origWidthHeightRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				parts := origWidthHeightRE.FindStringSubmatch(repl)
				if len(parts) != 3 {
					log.Printf("Error: invalid orig-width-height: %s", repl)
					return repl
				}

				return fmt.Sprintf(`width=%q height=%q`, parts[1], parts[2])
			})
			postHTML = origHeightWidthRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				parts := origHeightWidthRE.FindStringSubmatch(repl)
				if len(parts) != 3 {
					log.Printf("Error: invalid orig-width-height: %s", repl)
					return repl
				}

				return fmt.Sprintf(`width=%q height=%q`, parts[2], parts[1])
			})
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

				return fmt.Sprintf(`<img class="avatar" src=%q loading="lazy" /> <a href=%q>%s</a> (<a %shref=%q%s>post</a>):`, "/avatar/"+tumblrName, tumblrLink, tumblrName, parts[1], reblogLink, parts[4])
			})
			postHTML = tumblrAccountLinkRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				if strings.Contains(repl, "@tiktok") {
					return repl
				}
				parts := tumblrAccountLinkRE.FindStringSubmatch(repl)
				if len(parts) != 4 {
					log.Printf("Error: invalid tumblr account link: %s", repl)
					return repl
				}

				return fmt.Sprintf(`<a %shref=%q%s>%s</a>`, parts[1], "/"+parts[3], parts[2], "@"+parts[3])
			})
			postHTML = tumblrLinksRE.ReplaceAllStringFunc(postHTML, tumblrToInternal)
			postHTML = strings.Replace(postHTML, "https://href.li/?", "", -1)
			postHTML = instagramLinksRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				parts := instagramLinksRE.FindStringSubmatch(repl)
				if len(parts) != 3 {
					log.Printf("Error: invalid instagram link: %s", repl)
					return repl
				}
				return "/" + parts[2] + "@instagram"
			})

			postHTML = altTextRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				parts := altTextRE.FindStringSubmatch(repl)
				if len(parts) != 3 {
					log.Printf("Error: weird alt tag %q", repl)
					return repl
				}
				if parts[1] == "image" { // many images just have alt="image" which is not helpful
					return repl
				}

				res := repl
				if parts[1] != "" {
					res += ` title="` + parts[1] + `"`
				} else {
					res += ` title='` + parts[2] + `'`
				}
				return res
			})
			postHTML = strings.Replace(postHTML, `<span class="tmblr-alt-text-helper">ALT</span>`, "", -1)

			if post.Source != "tiktok" {
				postHTML = videoRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
					return `<video preload="metadata" controls="" `
				})
			}
			postHTML = autoplayRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
				return ``
			})

			for _, term := range search.Terms {
				termRE, err := regexp.Compile("(?i)(" + regexp.QuoteMeta(term) + ")")
				if err != nil {
					postHTML = strings.Replace(postHTML, term, "<mark>"+term+"</mark>", -1)
					continue
				}
				postHTML = termRE.ReplaceAllStringFunc(postHTML, func(repl string) string {
					return "<mark>" + repl + "</mark>"
				})
			}

			if isHidden {
				postHTML = fmt.Sprintf("<p>hidden by %q</p>", strings.TrimSpace(postFilter.String()))
			}

			fmt.Fprint(w, postHTML)

			fmt.Fprintln(w, `</section>`)

			fmt.Fprint(w, "<footer>")
			if len(post.Tags) > 0 {
				fmt.Fprint(w, `<ul class="tags">`)
				for i, tag := range post.Tags {
					if i == TagsCollapseCount {
						fmt.Fprintf(w, `<details><summary>...</summary> `)
					}

					tagFound := false
					for _, searchTag := range search.Tags {
						if strings.ToLower(tag) == strings.ToLower(searchTag) {
							tagFound = true
						}
					}

					tagLink := "/" + post.Author + "/tagged/" + tag
					tag = "#" + tag
					if tagFound {
						tag = "<mark>" + tag + "</mark>"
					}
					fmt.Fprintf(w, `<li><a href=%q>%s</a></li> `, tagLink, tag)
				}
				if len(post.Tags) >= TagsCollapseCount {
					fmt.Fprintf(w, `</details>`)
				}
				fmt.Fprintln(w, `</ul>`)
			}
			fmt.Fprintf(w, `<time title="%s" datetime="%s">%s ago</time> `, post.Date, post.DateString, prettyDuration(time.Since(post.Date)))
			fmt.Fprintf(w, `by <a href=%q>%s</a>, `, "/"+post.Author, post.Author)
			if post.Source == "tumblr" {
				fmt.Fprintf(w, `<a href=%q title="link to just this post">post</a> <a class="tumblr-link" href=%q>t</a>`, tumblrToInternal(post.URL), post.URL)
			} else {
				fmt.Fprintf(w, `<a href=%q title="link to just this post">post</a>`, post.URL)
			}
			fmt.Fprint(w, "</footer>")
			fmt.Fprintln(w, "</article>")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}

		if len(settings.SelectedFeeds) > 1 && len(group) >= GroupPostsNumber {
			fmt.Fprintln(w, `</details>`)
		}
	}

	fmt.Fprintln(w, `<span id="bottom"></span>
<a id="link-top" class="jumper" href="#">▴</a>`)

	if lastPost != nil {
		nextPage := req.URL
		query := url.Values{}
		query.Set("before", lastPost.ID)
		if req.URL.Query().Get("search") != "" {
			query.Set("search", req.URL.Query().Get("search"))
		}
		nextPage.RawQuery = query.Encode()
		fmt.Fprintf(w, `<div class="next-page"><a href="%s">next page</a></div>`, nextPage)
	}

	fmt.Fprintf(w, `<form method="POST" action="/settings">

	<input type="text" name="list" hidden value=%q />

	<label for="feeds">Feeds to view by default</label>:
	<div class="field">
		<textarea rows="%d" cols="30" name="feeds">%s</textarea>
	</div>
	<input type="submit" value="Save" />
</form>

<form method="POST" action="/settings/clear">
	<input type="submit" value="Clear" title="FIXME: clear currently broken :/" disabled />
</form>
`, mux.Vars(req)["list"], len(settings.SelectedFeeds)+1, strings.Join(settings.SelectedFeeds, "\n"))

	u := url.URL{
		Path: strings.Join(settings.SelectedFeeds, ","),
	}
	if strings.ContainsAny(u.Path, "/&?") {
		u.Path = "/"
		query := make(url.Values)
		query["feeds"] = settings.SelectedFeeds
		u.RawQuery = query.Encode()
	}
	fmt.Fprintf(w, `<p>Share feed via <a href=%q>a link</a>.</p>`, u.String())

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

	fmt.Fprintf(w, `<hr /><footer>%d posts from %q (<a href=%q>source</a>) in %s (open: %s)</footer>`, postCount, mergedFeeds.Name(), mergedFeeds.URL(), time.Since(start).Round(time.Millisecond), openTime.Round(time.Millisecond))

	feedsByTime := make([]string, 0, len(feedInfo))
	for feed := range feedInfo {
		feedsByTime = append(feedsByTime, feed)
	}
	sort.Sort(sort.Reverse(sortByFunc{strings: feedsByTime, lessFn: func(a, b string) bool { return feedInfo[a].Duration < feedInfo[b].Duration }}))
	fmt.Fprintln(w, `<details><summary>Performance details</summary><ol>`)
	for _, feedName := range feedsByTime {
		errorInfo := ""
		if feedInfo[feedName].Error != nil {
			errorInfo = fmt.Sprintf(" (<code style=\"font-size: smaller\">%s</code>)", feedInfo[feedName].Error)
		}
		notes := ""
		if feedWithNotes, ok := feedInfo[feedName].Feed.(feed.Notes); ok {
			notes = feedWithNotes.Notes()
			if notes != "" {
				notes = ", " + notes
			}
		}
		fmt.Fprintf(w, `<li>%s (%s%s)%s</li>`, feedName, feedInfo[feedName].Duration, notes, errorInfo)
	}
	fmt.Fprintln(w, `</ol></details>`)

	if err != nil && !errors.Is(err, io.EOF) {
		log.Println("decode:", err)
	}

	fmt.Fprintln(w, `<script>
  // pretty reloads (sparkly indicator)

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

  window.addEventListener("click", (ev) => {
    if (ev.target.tagName != "A" || new URL(ev.target.href).pathname == window.location.pathname) {
      return;
	 }
	 reloadSpinner();
  });
  window.addEventListener("focus", (ev) => {
    let reloadEl = document.querySelector("#reload");
    if (reloadEl) {
      document.body.removeChild(reloadEl);
	 }
  });

  // pull to reload

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

      reloadSpinner();
    }
  }, {passive: true});

  // skip posts with a double-tap

  let lastTouch = 0;
  window.addEventListener('mousedown', (ev) => {
    let el = ev.target.closest("article");
    if (ev.timeStamp - lastTouch < 500 && el != null) {
      ev.preventDefault();
      //window.scrollBy({top: el.clientHeight, behaviour: 'smooth'});
      window.scrollTo({top: el.offsetTop + el.clientHeight - (window.innerHeight * 0.1), behavior: 'auto'});
    };
    lastTouch = ev.timeStamp;
  });

  window.addEventListener('click', (ev) => {
    if (ev.ctrlKey) {
      let postLink = ev.target.closest('article').querySelector('a[title="link to just this post"]').href;
      window.location = postLink;
    }
  });

  // service worker to be detected as a progressive web app in webkit-based browsers

  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/service-worker.js')
      .then(() => console.log("numblr registered!"))
		.catch((err) => console.log("numblr registration failed: ", err));
  }
</script>`)

	fmt.Fprintln(w, `</div>`)

	fmt.Fprintln(w, `</body>
</html>`)
}

func nextPostsGroup(posts []*feed.Post, groupPostsNumber int) (group []*feed.Post, rest []*feed.Post) {
	if len(posts) == 0 || len(posts) == 1 {
		return posts, nil
	}

	i := 0
	for ; i+1 < len(posts); i++ {
		if posts[i].Author != posts[i+1].Author {
			break
		}
	}

	if i+1 >= groupPostsNumber {
		return posts[:i+1], posts[i+1:]
	}

	return []*feed.Post{posts[0]}, posts[1:]
}

func tumblrToInternal(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		log.Printf("could not parse url: %s", err)
		return link
	}

	if u.Path == "/redirect" {
		redirect := u.Query().Get("z")
		if redirect == "" {
			log.Printf("invalid redirect: %q", link)
			return link
		}

		return redirect
	}

	tumblrName := u.Host[:strings.Index(u.Host, ".")]
	u.Host = ""
	u.Scheme = ""
	u.Path = path.Join("/", tumblrName, u.Path)
	return u.String()
}

type Settings struct {
	// SelectedFeeds are the feeds that are explicitely selected, e.g. on
	// the index page, by specifying feeds in the url, or by being on a
	// list page.
	SelectedFeeds []string

	// Searches are feed-specific searches that apply additional filters to
	// specific feeds, e.g. when you want to filter out or filter on things
	// from those feeds.
	Searches map[string]feed.Search

	// GlobalSearch is a persistent search that applies to all feeds.
	GlobalSearch feed.Search
}

func SettingsFromRequest(req *http.Request) Settings {
	settings := Settings{}

	feeds := getFeeds(req)
	settings.SelectedFeeds = make([]string, 0, len(feeds))
	settings.Searches = make(map[string]feed.Search)

	for _, feedName := range feeds {
		splitAt := 0
		// if @xyz in feedName, split after occurence of first @
		atIdx := strings.Index(feedName, "@")
		if atIdx != -1 {
			splitAt = atIdx
		}

		spaceIdx := strings.Index(feedName[splitAt:], " ")
		var name string
		var search string
		if spaceIdx == -1 {
			name = feedName
			search = ""
		} else {
			name = feedName[:splitAt+spaceIdx]
			search = feedName[splitAt+spaceIdx+1:]
		}
		fmt.Printf("%q filtered by %q\n", name, search)
		if search != "" {
			s := feed.ParseTerms(search)

			if name == "*" {
				settings.GlobalSearch = s
				continue
			}

			settings.Searches[name] = s
		}

		settings.SelectedFeeds = append(settings.SelectedFeeds, name)
	}

	return settings
}

func getFeeds(req *http.Request) []string {
	isList := strings.HasPrefix(req.URL.Path, "/list/")

	if req.URL.Query()["feeds"] != nil && len(req.URL.Query()["feeds"]) > 0 {
		return req.URL.Query()["feeds"]
	}

	// explicitely specified in url
	feeds := req.URL.Path[1:]
	if feeds != "" && !isList {
		return strings.Split(feeds, ",")
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
		return strings.Split(config.DefaultFeed, ",")
	}

	if cookie.Value != "" {
		return strings.Split(cookie.Value, ",")
	}

	return strings.Split(config.DefaultFeed, ",")
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
	postID := mux.Vars(req)["postId"]
	slug := mux.Vars(req)["slug"]
	if slug != "" {
		slug = "/" + slug
	}
	tumblrURL := fmt.Sprintf("https://%s.tumblr.com/post/%s%s", tumblr, postID, slug)
	req, err := http.NewRequestWithContext(req.Context(), "GET", tumblrURL, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: could not create request: %s", err), http.StatusInternalServerError)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: could not fetch post: %s", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// TODO: bring back special styles (?)
	htmlPrelude(w, req, fmt.Sprintf("%s - %s", tumblr, slug), req.URL.String(), "/avatar/"+url.PathEscape(tumblr))

	node, err := html.Parse(resp.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: could not parse post: %s", err), http.StatusInternalServerError)
		return
	}

	var cleanup func(*html.Node)
	cleanup = func(node *html.Node) {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode {
				switch child.Data {
				case "section":
					if hasAttribute(child, "class", "related-posts-wrapper") {
						node.RemoveChild(child)
						continue
					}
					if hasAttribute(child, "class", "post-controls") {
						node.RemoveChild(child)
						continue
					}
				case "iframe":
					for _, attr := range child.Attr {
						if attr.Key == "src" && strings.Contains(attr.Val, "/photoset_iframe/") {
							photosetImages, err := fetchPhotoset(req.Context(), tumblr, attr.Val)
							if err != nil {
								log.Printf("Error: Invalid photoset %q: %s", attr.Val, err)
								break
							}

							for _, img := range photosetImages {
								node.InsertBefore(img, child)
							}

							break
						}
						if attr.Key == "src" && strings.Contains(attr.Val, "/video/") {
							videos, err := fetchVideo(req.Context(), tumblr, "/post/"+postID+slug, attr.Val)
							if err != nil {
								log.Printf("Error: Invalid video %q: %s", attr.Val, err)
								break
							}

							for _, video := range videos {
								node.InsertBefore(video, child)
							}

							break
						}
						if attr.Key == "src" && strings.Contains(attr.Val, "/audio_player_iframe/") {
							u, err := url.Parse(attr.Val)
							if err != nil {
								log.Printf("Error: Invalid audio player %q: %s", attr.Val, err)
								break
							}
							audioURL := u.Query().Get("audio_file")
							audio := html.Node{
								Type: html.ElementNode,
								Data: "audio",
								Attr: []html.Attribute{
									{Key: "src", Val: audioURL},
									{Key: "controls"},
								},
							}
							node.InsertBefore(&audio, child)
							break
						}
					}

					node.RemoveChild(child)
					continue
				case "script", "style", "form":
					node.RemoveChild(child)
					continue
				case "figure":
					attrs := make([]html.Attribute, 0, 2)
					for _, attr := range child.Attr {
						switch attr.Key {
						case "data-orig-width", "data-orig-height":
							attrs = append(attrs, attr)
						}
					}
					child.Attr = attrs
				case "video":
					attrs := []html.Attribute{{Key: "preload", Val: "metadata"}, {Key: "controls"}}
					for _, attr := range child.Attr {
						switch attr.Key {
						case "poster", "muted":
							attrs = append(attrs, attr)
						}
					}
					if child.Parent.Type == html.ElementNode && child.Parent.Data == "figure" {
						for _, attr := range child.Parent.Attr {
							switch attr.Key {
							case "data-orig-width":
								attr.Key = "width"
								attrs = append(attrs, attr)
							case "data-orig-height":
								attr.Key = "height"
								attrs = append(attrs, attr)
							}
						}
					}
					child.Attr = attrs
				default:
					attrs := make([]html.Attribute, 0, len(child.Attr))
					for _, attr := range child.Attr {
						switch attr.Key {
						case "href":
							if strings.Contains(attr.Val, ".tumblr.com") {
								attr.Val = tumblrToInternal(attr.Val)
							} else if strings.HasPrefix(attr.Val, "https://href.li/?") {
								attr.Val = attr.Val[len("https://href.li/?"):]
							} else if attr.Val == "/" {
								attr.Val = "/" + tumblr
							} else if strings.HasPrefix(attr.Val, "/") {
								attr.Val = "/" + tumblr + attr.Val
							}
							attrs = append(attrs, attr)
						case "src", "rel", "title", "alt", "class", "style":
							attrs = append(attrs, attr)
						}
					}
					child.Attr = attrs
				}
			}

			cleanup(child)
		}
	}

	var f func(*html.Node)
	f = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "a", "p", "img", "div", "span":
				if hasAttribute(node, "class", "app-nag") {
					return
				}

				cleanup(node)

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

	fmt.Fprintf(w, `<hr />
<p><a href=%q>View on Tumblr</a></p>
<p><a href=%q>View on archive.org</a></p>
`, tumblrURL, fmt.Sprintf("https://web.archive.org/web/%s/%s", time.Now().Format("20060102"), tumblrURL))
}

func fetchPhotoset(ctx context.Context, tumblr string, photosetPath string) ([]*html.Node, error) {
	u, err := url.Parse(photosetPath)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	u.Scheme = "https"
	u.Host = tumblr + ".tumblr.com"
	u.Path = strings.Replace(u.Path, "/0/", "/512/", 1)

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	node, err := html.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	nodes := make([]*html.Node, 0, 10)

	var f func(*html.Node)
	f = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "img":
				nodes = append(nodes, node)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && child.Data == "img" {
				filterAttributes(child, "src")
				node.RemoveChild(child)
				nodes = append(nodes, child)
				nodes = append(nodes, &html.Node{Type: html.ElementNode, Data: "br"})
				continue
			}

			f(child)
		}
	}
	f(node)

	return nodes, nil
}

func fetchVideo(ctx context.Context, tumblr string, postPath string, videoPath string) ([]*html.Node, error) {
	u, err := url.Parse(videoPath)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	u.Scheme = "https"
	u.Host = tumblr + ".tumblr.com"

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Referer", "https://"+u.Host+postPath)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	node, err := html.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	nodes := make([]*html.Node, 0, 10)

	var f func(*html.Node)
	f = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "video":
				nodes = append(nodes, node)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && child.Data == "video" {
				filterAttributes(child, "src", "poster")
				child.Attr = append(child.Attr, html.Attribute{Key: "preload"})
				child.Attr = append(child.Attr, html.Attribute{Key: "controls"})
				node.RemoveChild(child)
				nodes = append(nodes, child)
				nodes = append(nodes, &html.Node{Type: html.ElementNode, Data: "br"})
				continue
			}

			f(child)
		}
	}
	f(node)

	return nodes, nil
}

func hasAttribute(node *html.Node, attrName, attrValue string) bool {
	for _, attr := range node.Attr {
		if attr.Key == attrName && attr.Val == attrValue {
			return true
		}
	}
	return false
}

func filterAttributes(node *html.Node, keepAttrs ...string) {
	attrs := make([]html.Attribute, 0, len(node.Attr))
	for _, attr := range node.Attr {
		for _, keep := range keepAttrs {
			if attr.Key == keep {
				attrs = append(attrs, attr)
			}
		}
	}
	node.Attr = attrs
}

type sortByFunc struct {
	strings []string
	lessFn  func(a, b string) bool
}

func (sbf sortByFunc) Len() int           { return len(sbf.strings) }
func (sbf sortByFunc) Less(i, j int) bool { return sbf.lessFn(sbf.strings[i], sbf.strings[j]) }
func (sbf sortByFunc) Swap(i, j int)      { sbf.strings[i], sbf.strings[j] = sbf.strings[j], sbf.strings[i] }
