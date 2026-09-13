package proxy

import "strings"

const providerHeader = "[model_providers.xai-oauth]"

func providerTable(baseURL, envKey string) string {
	s := providerHeader + "\nname = \"xAI Grok OAuth (local)\"\nbase_url = \"" + baseURL + "\"\nwire_api = \"responses\"\n"
	if envKey != "" {
		s += "env_key = \"" + envKey + "\"\n"
	}
	return s
}

func UpsertProvider(src, baseURL, envKey string) string {
	table := providerTable(baseURL, envKey)
	lines := strings.SplitAfter(src, "\n")
	start, end, found := findTable(lines, providerHeader)
	if !found {
		out := strings.TrimRight(src, "\n")
		if out != "" {
			out += "\n\n"
		}
		return out + table
	}
	return strings.Join(lines[:start], "") + table + strings.Join(lines[end:], "")
}

func findTable(lines []string, header string) (start, end int, found bool) {
	for i, ln := range lines {
		trim := strings.TrimSpace(strings.TrimSuffix(ln, "\r"))
		if trim != header {
			continue
		}
		start = i
		end = len(lines)
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimSpace(strings.TrimSuffix(lines[j], "\r"))
			if strings.HasPrefix(t, "[") {
				end = j
				break
			}
		}
		return start, end, true
	}
	return 0, 0, false
}
