package proxy

import "strings"

const providerHeader = "[model_providers.xai-oauth]"

func providerTable(baseURL, envKey string) string {
	var b strings.Builder
	b.WriteString(providerHeader)
	b.WriteString("\nname = \"xAI Grok OAuth (local)\"\n")
	b.WriteString("base_url = \"")
	b.WriteString(baseURL)
	b.WriteString("\"\nwire_api = \"responses\"\n")
	if envKey != "" {
		b.WriteString("env_key = \"")
		b.WriteString(envKey)
		b.WriteString("\"\n")
	}
	return b.String()
}

func UpsertProvider(src, baseURL, envKey string) string {
	table := providerTable(baseURL, envKey)
	lines := splitKeepEnd(src)
	start, end, found := findTable(lines, providerHeader)
	if !found {
		out := strings.TrimRight(src, "\n")
		if out != "" {
			out += "\n\n"
		}
		return out + table
	}
	var b strings.Builder
	for i := 0; i < start; i++ {
		b.WriteString(lines[i])
	}
	b.WriteString(table)
	for i := end; i < len(lines); i++ {
		b.WriteString(lines[i])
	}
	return b.String()
}

func splitKeepEnd(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			lines = append(lines, s)
			break
		}
		lines = append(lines, s[:i+1])
		s = s[i+1:]
		if s == "" {
			break
		}
	}
	return lines
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
