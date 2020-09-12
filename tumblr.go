package main

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

func FlattenReblogs(reblogHTML string) (flattenedHTML string, err error) {
	node, err := html.Parse(strings.NewReader(reblogHTML))
	if err != nil {
		return reblogHTML, fmt.Errorf("parse html: %w", err)
	}

	var root *html.Node

	var f func(*html.Node, *html.Node)
	f = func(parent *html.Node, node *html.Node) {
		if isElement(node, "p") && isElement(nextElementSibling(node), "blockquote") { // p blockquote
			reblog := nextElementSibling(node)
			reblogChild := firstElementChild(reblog)
			reblogContent := nextElementSibling(reblogChild)

			if root == nil {
				root = reblog.Parent
			}

			if isElement(reblogChild, "p") && isElement(reblogContent, "blockquote") { // p blockquote > (p blockquote)
				if parent != nil {
					parent.RemoveChild(node)
				}

				reblogContent.Parent.InsertBefore(node, reblogContent.NextSibling)

				f(reblog, reblogChild)
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			f(node, child)
		}
	}
	f(nil, node)

	if root == nil {
		return reblogHTML, fmt.Errorf("invalid reblog structure: %q", reblogHTML)
	}

	buf := new(bytes.Buffer)
	for node := root; node != nil; node = node.NextSibling {
		err = html.Render(buf, root)
		if err != nil {
			return reblogHTML, fmt.Errorf("render html: %w", err)
		}
	}

	return buf.String(), nil
}

func nextElementSibling(node *html.Node) *html.Node {
	for next := node.NextSibling; next != nil; next = next.NextSibling {
		if next.Type == html.ElementNode {
			return next
		}
	}
	return nil
}

func firstElementChild(node *html.Node) *html.Node {
	for next := node.FirstChild; next != nil; next = next.NextSibling {
		if next.Type == html.ElementNode {
			return next
		}
	}
	return nil
}

func isElement(node *html.Node, element string) bool {
	return node != nil && node.Type == html.ElementNode && node.Data == element
}
