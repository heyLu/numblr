package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNextPostsGroup(t *testing.T) {
	testCases := []struct {
		author            string
		groupSize         int
		posts             []*Post
		numGroup, numRest int
	}{
		{"a", 3, []*Post{&Post{Author: "a"}, &Post{Author: "a"}, &Post{Author: "a"}, &Post{Author: "b"}, &Post{Author: "c"}, &Post{Author: "d"}}, 3, 3},
		{"a", 3, []*Post{&Post{Author: "a"}, &Post{Author: "x"}, &Post{Author: "a"}, &Post{Author: "b"}, &Post{Author: "c"}, &Post{Author: "d"}}, 1, 5},
		{"a", 3, []*Post{&Post{Author: "a"}, &Post{Author: "a"}, &Post{Author: "a"}}, 3, 0},
	}

	for _, tc := range testCases {
		t.Run(tc.author, func(t *testing.T) {
			group, rest := nextPostsGroup(tc.posts, tc.groupSize)
			assert.Equal(t, tc.numGroup, len(group), "group")
			assert.Equal(t, tc.numRest, len(rest), "group")

			for _, post := range group {
				assert.Equal(t, tc.author, post.Author, "group author")
			}

			if len(rest) > 0 {
				assert.NotEqual(t, tc.author, rest[0].Author, "rest author")
			}
		})
	}
}
