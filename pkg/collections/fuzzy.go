package collections

import (
	"sort"
	"strings"
)

func FindSimilar(target string, candidates []string, topN int) []string {
	if len(candidates) == 0 {
		return nil
	}

	type match struct {
		name     string
		distance int
	}

	targetLower := strings.ToLower(target)
	matches := make([]match, 0, len(candidates))
	for _, c := range candidates {
		matches = append(matches, match{name: c, distance: Levenshtein(targetLower, strings.ToLower(c))})
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].distance < matches[j].distance })

	n := min(topN, len(matches))
	result := make([]string, n)
	for i := range n {
		result[i] = matches[i].name
	}
	return result
}

func Levenshtein(s1, s2 string) int {
	if len(s1) == 0 {
		return len(s2)
	}
	if len(s2) == 0 {
		return len(s1)
	}

	matrix := make([][]int, len(s1)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(s2)+1)
		matrix[i][0] = i
	}
	for j := range len(s2) + 1 {
		matrix[0][j] = j
	}

	for i := 1; i <= len(s1); i++ {
		for j := 1; j <= len(s2); j++ {
			cost := 1
			if s1[i-1] == s2[j-1] {
				cost = 0
			}
			matrix[i][j] = min(matrix[i-1][j]+1, min(matrix[i][j-1]+1, matrix[i-1][j-1]+cost))
		}
	}
	return matrix[len(s1)][len(s2)]
}
