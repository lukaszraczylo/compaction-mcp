package main

// EstimateTokens returns a rough token count for a string.
// Uses ~4 characters per token heuristic.
func EstimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len(s) + 3) / 4
}
