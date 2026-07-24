package cmd

import (
	"regexp"
	"sort"
	"strings"
)

var mergePunctuation = regexp.MustCompile(`[^a-z0-9]+`)

// mergeKey is a candidate cross-source name key, never an identity proof.
func mergeKey(name, number string) string {
	keys := mergeKeys(name, number)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func mergeKeys(name, number string) []string {
	base := strings.ToLower(strings.TrimSpace(name))
	base = mergePunctuation.ReplaceAllString(base, " ")
	base = strings.Join(strings.Fields(base), " ")
	if base == "" && strings.TrimSpace(number) == "" {
		return nil
	}
	variants := []string{base}
	for _, suffix := range []string{" railway station", " station", " stop", " platform"} {
		if strings.HasSuffix(base, suffix) {
			variants = append(variants, strings.TrimSpace(strings.TrimSuffix(base, suffix)))
		}
	}
	if n := strings.TrimSpace(strings.ToLower(number)); n != "" {
		for i := range variants {
			variants[i] = strings.TrimSpace(n + " " + variants[i])
		}
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(variants))
	for _, value := range variants {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
