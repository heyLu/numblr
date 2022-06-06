package feed

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseTerms(t *testing.T) {
	testCases := []struct {
		raw    string
		search Search
	}{
		// single words
		{`fun stuff here #and #tags #also`, Search{Terms: []string{"fun", "stuff", "here"}, Tags: []string{"and", "tags", "also"}, ExcludeTerms: []string{}, ExcludeTags: []string{}}},
		// quoted things
		{`"fun stuff here"`, Search{Terms: []string{"fun stuff here"}, Tags: []string{}, ExcludeTerms: []string{}, ExcludeTags: []string{}}},
		{`'fun stuff here'`, Search{Terms: []string{"fun stuff here"}, Tags: []string{}, ExcludeTerms: []string{}, ExcludeTags: []string{}}},
		{`nospaces "fun stuff here" morenospaces`, Search{Terms: []string{"nospaces", "fun stuff here", "morenospaces"}, Tags: []string{}, ExcludeTerms: []string{}, ExcludeTags: []string{}}},
		{`nospaces 'fun stuff here' morenospaces`, Search{Terms: []string{"nospaces", "fun stuff here", "morenospaces"}, Tags: []string{}, ExcludeTerms: []string{}, ExcludeTags: []string{}}},
		{`"one quoted" not quoted "two quoted" "three quoted"`, Search{Terms: []string{"one quoted", "not", "quoted", "two quoted", "three quoted"}, Tags: []string{}, ExcludeTerms: []string{}, ExcludeTags: []string{}}},
		{`"'"`, Search{Terms: []string{"'"}, Tags: []string{}, ExcludeTerms: []string{}, ExcludeTags: []string{}}},
		{`unmatched " quotes are a thing`, Search{Terms: []string{"unmatched", "\"", "quotes", "are", "a", "thing"}, Tags: []string{}, ExcludeTerms: []string{}, ExcludeTags: []string{}}},
		// exclusions
		{`-excluded`, Search{Terms: []string{}, Tags: []string{}, ExcludeTerms: []string{"excluded"}, ExcludeTags: []string{}}},
		{`-multiple -excluded`, Search{Terms: []string{}, Tags: []string{}, ExcludeTerms: []string{"multiple", "excluded"}, ExcludeTags: []string{}}},
		{`-"quoted stuff" -excluded`, Search{Terms: []string{}, Tags: []string{}, ExcludeTerms: []string{"quoted stuff", "excluded"}, ExcludeTags: []string{}}},
		{`mixed -"quoted stuff" -excluded "and not"`, Search{Terms: []string{"mixed", "and not"}, Tags: []string{}, ExcludeTerms: []string{"quoted stuff", "excluded"}, ExcludeTags: []string{}}},
		// tags
		{`#tags #work`, Search{Terms: []string{}, Tags: []string{"tags", "work"}, ExcludeTerms: []string{}, ExcludeTags: []string{}}},
		{`#tags #work -#including-exclusions`, Search{Terms: []string{}, Tags: []string{"tags", "work"}, ExcludeTerms: []string{}, ExcludeTags: []string{"including-exclusions"}}},
		{`#"multiple word tags" can be hacked`, Search{Terms: []string{"can", "be", "hacked"}, Tags: []string{"multiple word tags"}, ExcludeTerms: []string{}, ExcludeTags: []string{}}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.raw, func(t *testing.T) {
			search := ParseTerms(testCase.raw)
			require.Equal(t, testCase.search.Terms, search.Terms, "terms not equal")
			require.Equal(t, testCase.search.Tags, search.Tags, "tags not equal")
			require.Equal(t, testCase.search.ExcludeTerms, search.ExcludeTerms, "excluded terms not equal")
			require.Equal(t, testCase.search.ExcludeTags, search.ExcludeTags, "excluded tags not equal")
		})
	}
}
