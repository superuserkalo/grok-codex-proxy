package main

import "strings"

func MapModel(slug string) string {
	for _, p := range []string{"xai/", "grok-oauth/"} {
		slug = strings.TrimPrefix(slug, p)
	}
	return slug
}
