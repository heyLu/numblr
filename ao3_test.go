package main

import (
	"time"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAO3AuthorFandomFeed(t *testing.T) {
	feed, err := NewAO3("https://archiveofourown.org/users/astolat/works?fandom_id=136512", Search{})
	assert.NoError(t, err, "new")

	assert.Len(t, feed.(*ao3).works, 20)

	post, err := feed.Next()
	assert.NoError(t, err, "next")

	assert.Equal(t, "7756009", post.ID, "id")
	assert.Equal(t, "https://archiveofourown.org/works/7756009", post.URL, "url")
	assert.Equal(t, "13 Aug 2016", post.DateString, "date string")
	assert.Equal(t, time.Date(2016, time.August, 13, 0, 0, 0, 0, time.UTC), post.Date, "date")
	assert.Equal(t, "<h1>[VID] You Are A Runner And I Am My Father's Son by astolat</h1>", post.Title, "title")
	assert.Equal(t, "astolat", post.Author, "author")
	assert.Equal(t, "<p>I&#39;ll draw three figures on your heart.</p>", post.DescriptionHTML, "description")
	assert.Equal(t, []string{"Creator Chose Not To Use Archive Warnings","Draco Malfoy/Harry Potter","Draco Malfoy", "Harry Potter", "Vividcon", "Vividcon 2016", "Vividcon 2016 Premieres"}, post.Tags, "tags")
}