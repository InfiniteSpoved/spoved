// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package test

import (
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

var brandRe = regexp.MustCompile(`.{0,40}(Forgejo|GitHat).{0,40}`)

// BrandMentions returns every user-visible "Forgejo" or "GitHat" in an HTML page or mail, with some context.
// It reads the title, meta contents, body text and the alt, title, placeholder and aria-label attributes,
// and ignores the "based on Forgejo" credit link (a.codehat-credit).
func BrandMentions(html string) []string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return []string{err.Error()}
	}
	doc.Find("a.codehat-credit, script, style").Remove()
	texts := []string{doc.Find("title").Text(), doc.Find("body").Text()}
	doc.Find("meta[content], [alt], [title], [placeholder], [aria-label]").Each(func(_ int, s *goquery.Selection) {
		for _, attr := range []string{"content", "alt", "title", "placeholder", "aria-label"} {
			if v, ok := s.Attr(attr); ok {
				texts = append(texts, v)
			}
		}
	})
	var hits []string
	for _, text := range texts {
		for _, hit := range brandRe.FindAllString(text, -1) {
			hits = append(hits, strings.Join(strings.Fields(hit), " "))
		}
	}
	return hits
}
