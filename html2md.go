package main

import (
	_ "embed"
	"strings"

	md "github.com/JohannesKaufmann/html-to-markdown"
)

//go:embed helpers.go.txt
var helpersSrc string

// HTMLToMarkdown converts LeetCode description HTML to clean markdown.
func HTMLToMarkdown(html string) string {
	converter := md.NewConverter("", true, &md.Options{})
	out, err := converter.ConvertString(html)
	if err != nil {
		return stripTags(html)
	}
	out = strings.TrimSpace(out)
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return out
}

func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}