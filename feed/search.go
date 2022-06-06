package feed

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Search represents a search in a feed.
type Search struct {
	IsActive bool

	BeforeID string

	NoReblogs    bool
	Skip         bool
	Terms        []string
	Tags         []string
	ExcludeTerms []string
	ExcludeTags  []string

	ForceFresh bool

	termsRE         *regexp.Regexp
	excludedTermsRE *regexp.Regexp
}

func (s *Search) String() string {
	if !s.IsActive {
		return ""
	}

	buf := new(bytes.Buffer)
	if s.NoReblogs {
		fmt.Fprint(buf, " noreblogs")
	}
	for _, term := range s.Terms {
		fmt.Fprint(buf, " "+term)
	}
	for _, term := range s.ExcludeTerms {
		fmt.Fprint(buf, " -"+term)
	}
	for _, tag := range s.Tags {
		fmt.Fprint(buf, " #"+tag)
	}
	for _, tag := range s.ExcludeTags {
		fmt.Fprint(buf, " -#"+tag)
	}

	return buf.String()
}

// Matches returns true if the post matches the search.
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

	if s.termsRE != nil {
		if !s.termsRE.MatchString(p.Title) && !s.termsRE.MatchString(p.DescriptionHTML) {
			return false
		}
	} else {
		for _, term := range s.Terms {
			if !strings.Contains(strings.ToLower(p.Title), term) && !strings.Contains(strings.ToLower(p.DescriptionHTML), term) {
				return false
			}
		}
	}

	if s.excludedTermsRE != nil {
		if s.excludedTermsRE.MatchString(p.Title) || s.excludedTermsRE.MatchString(p.DescriptionHTML) {
			return false
		}

	} else {
		for _, term := range s.ExcludeTerms {
			if strings.Contains(strings.ToLower(p.Title), term) || strings.Contains(strings.ToLower(p.DescriptionHTML), term) {
				return false
			}
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

// FromRequest parses search info from the request.
//
// Search.IsActive if there is a search happening.
func FromRequest(req *http.Request) Search {
	beforeParam := req.URL.Query().Get("before")
	forceFresh := req.URL.Query().Get("fresh") != ""

	rawSearch := req.URL.Query().Get("search")
	if beforeParam == "" && rawSearch == "" {
		return Search{ForceFresh: forceFresh}
	}

	search := ParseTerms(rawSearch)
	search.BeforeID = beforeParam
	search.ForceFresh = forceFresh

	return search
}

const quoteChars = `"'`

// ParseTerms parses the search terms from the given string.
func ParseTerms(rawSearch string) Search {
	search := Search{
		IsActive:     true,
		Terms:        make([]string, 0, 1),
		Tags:         make([]string, 0, 1),
		ExcludeTags:  make([]string, 0, 1),
		ExcludeTerms: make([]string, 0, 1),
	}

	for len(rawSearch) > 0 {
		exclude := false
		if len(rawSearch) > 0 && rawSearch[0] == '-' {
			exclude = true
			rawSearch = rawSearch[1:]
		}

		tag := false
		if len(rawSearch) > 0 && rawSearch[0] == '#' {
			tag = true
			rawSearch = rawSearch[1:]
		}

		var searchTerm string

		spaceIdx := strings.IndexAny(rawSearch, ` `)
		quoteIdx := strings.IndexAny(rawSearch, quoteChars)

		if quoteIdx == -1 {
			if spaceIdx == -1 {
				searchTerm = rawSearch
				rawSearch = ""
			} else {
				searchTerm = rawSearch[:spaceIdx]
				rawSearch = rawSearch[spaceIdx+1:]
			}
		} else {
			if spaceIdx != -1 && spaceIdx < quoteIdx { // space before quote
				searchTerm = rawSearch[:spaceIdx]
				rawSearch = rawSearch[spaceIdx+1:]
			} else {
				nextQuoteIdx := strings.IndexByte(rawSearch[quoteIdx+1:], rawSearch[quoteIdx]) // matching quote
				if nextQuoteIdx == -1 {                                                        // unmatched quote
					if spaceIdx == -1 {
						searchTerm = rawSearch
						rawSearch = ""
					} else {
						searchTerm = rawSearch[:spaceIdx]
						rawSearch = rawSearch[spaceIdx+1:]
					}
				} else {
					searchTerm = rawSearch[quoteIdx+1 : nextQuoteIdx+1]
					rawSearch = rawSearch[nextQuoteIdx+2:]
					fmt.Printf("%q %q\n", searchTerm, rawSearch)
				}
			}
		}

		if len(searchTerm) == 0 {
			continue
		}

		if searchTerm == "noreblog" || searchTerm == "noreblogs" {
			search.NoReblogs = true
			continue
		}
		if searchTerm == "skip" {
			search.Skip = true
			continue
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

	if len(search.Terms) > 0 {
		termsRE, err := regexp.Compile(`(?i)\b(` + strings.Join(search.Terms, "|") + `)\b`)
		if err == nil {
			search.termsRE = termsRE
		} else {
			log.Printf("invalid search terms %q: %s", search.Terms, err)
		}
	}
	if len(search.ExcludeTerms) > 0 {
		excludedTermsRE, err := regexp.Compile(`(?i)\b(` + strings.Join(search.ExcludeTerms, "|") + `)\b`)
		if err == nil {
			search.excludedTermsRE = excludedTermsRE
		} else {
			log.Printf("invalid exclude terms %q: %s", search.ExcludeTerms, err)
		}
	}

	return search
}
