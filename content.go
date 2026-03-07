package main

import (
	"path/filepath"
	"regexp"
	"strings"
)

// ContentType classifies stored content for scoring and decay tuning.
type ContentType int

const (
	ContentProse ContentType = iota
	ContentCode
	ContentError
	ContentToolOutput
	ContentDecision
)

var (
	errorPatterns = []string{
		"error:", "Error:", "panic:", "FAIL", "goroutine",
		"Exception", "Traceback",
	}

	// Stack trace pattern: file.go:123 or File.java:45
	stackTraceRe = regexp.MustCompile(`\w+\.\w+:\d+`)

	codeKeywords = []string{
		"func ", "class ", "def ", "import ", "package ", "#include",
	}

	decisionKeywords = []string{
		"decided", "agreed", "will use", "chosen",
		"approach:", "decision:", "going with",
	}

	fileExtMap = map[string]string{
		".go":   "go",
		".ts":   "typescript",
		".py":   "python",
		".yaml": "yaml",
		".yml":  "yaml",
		".json": "json",
		".rs":   "rust",
		".jsx":  "react",
		".tsx":  "react",
	}

	infraKeywords = []string{
		"kubernetes", "docker", "cilium", "postgres",
		"nginx", "redis", "graphql", "terraform",
	}

	urlRe      = regexp.MustCompile(`https?://\S+`)
	filePathRe = regexp.MustCompile(`(?:\s|^)/?(?:[\w.-]+/){2,}[\w.-]+`)
)

const maxTags = 5

// DetectContentType returns the content classification using priority:
// Error > Code > Decision > ToolOutput > Prose.
func DetectContentType(content string) ContentType {
	if isError(content) {
		return ContentError
	}
	if isCode(content) {
		return ContentCode
	}
	if isDecision(content) {
		return ContentDecision
	}
	if isToolOutput(content) {
		return ContentToolOutput
	}
	return ContentProse
}

func isError(content string) bool {
	for _, p := range errorPatterns {
		if strings.Contains(content, p) {
			return true
		}
	}
	return stackTraceRe.FindString(content) != "" && strings.Contains(content, "\n")
}

func isCode(content string) bool {
	if strings.Contains(content, "```") {
		return true
	}
	for _, kw := range codeKeywords {
		if strings.Contains(content, kw) {
			return true
		}
	}
	return bracketDensity(content) > 0.05
}

func bracketDensity(content string) float64 {
	if len(content) == 0 {
		return 0
	}
	count := 0
	for _, c := range content {
		switch c {
		case '{', '}', '(', ')', '[', ']':
			count++
		}
	}
	return float64(count) / float64(len([]rune(content)))
}

func isDecision(content string) bool {
	lower := strings.ToLower(content)
	for _, kw := range decisionKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func isToolOutput(content string) bool {
	if strings.HasPrefix(content, "$ ") || strings.HasPrefix(content, "> ") {
		return true
	}
	for _, ch := range []string{"───", "│", "├"} {
		if strings.Contains(content, ch) {
			return true
		}
	}
	matches := filePathRe.FindAllString(content, -1)
	words := strings.Fields(content)
	if len(words) > 0 && float64(len(matches))/float64(len(words)) > 0.3 {
		return true
	}
	return false
}

// AutoTags extracts up to 5 deduplicated tags from content.
func AutoTags(content string) []string {
	seen := make(map[string]struct{})
	var tags []string

	add := func(tag string) {
		if len(tags) >= maxTags {
			return
		}
		lower := strings.ToLower(tag)
		if _, ok := seen[lower]; ok {
			return
		}
		seen[lower] = struct{}{}
		tags = append(tags, lower)
	}

	// Content type tag
	ct := DetectContentType(content)
	name := ContentTypeName(ct)
	if name != "prose" {
		add(name)
	}

	// File extension tags
	words := strings.Fields(content)
	for _, w := range words {
		ext := filepath.Ext(strings.TrimRight(w, ",:;)\"'`"))
		if tag, ok := fileExtMap[ext]; ok {
			add(tag)
		}
	}

	// Infrastructure keyword tags
	lower := strings.ToLower(content)
	for _, kw := range infraKeywords {
		if strings.Contains(lower, kw) {
			add(kw)
		}
	}

	// URL tag
	if urlRe.MatchString(content) {
		add("reference")
	}

	return tags
}

// ScoreMultiplier returns an importance multiplier based on content type.
func ScoreMultiplier(ct ContentType) float64 {
	switch ct {
	case ContentError:
		return 1.5
	case ContentDecision:
		return 1.3
	case ContentCode:
		return 1.2
	case ContentToolOutput:
		return 0.7
	default:
		return 1.0
	}
}

// DecayHalfLifeMinutes returns the recency half-life in minutes for a content type.
func DecayHalfLifeMinutes(ct ContentType) float64 {
	switch ct {
	case ContentError:
		return 30
	case ContentDecision:
		return 360
	case ContentCode:
		return 360
	case ContentProse:
		return 120
	case ContentToolOutput:
		return 15
	default:
		return 120
	}
}

// ContentTypeName returns the human-readable name for a content type.
func ContentTypeName(ct ContentType) string {
	switch ct {
	case ContentProse:
		return "prose"
	case ContentCode:
		return "code"
	case ContentError:
		return "error"
	case ContentToolOutput:
		return "tool-output"
	case ContentDecision:
		return "decision"
	default:
		return "prose"
	}
}
