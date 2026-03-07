package main

import (
	"strings"
	"testing"
)

func TestDetectContentType(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    ContentType
	}{
		{
			name:    "error with Error: prefix",
			content: "Error: connection refused to postgres on port 5432",
			want:    ContentError,
		},
		{
			name:    "error with panic",
			content: "panic: runtime error: index out of range [3] with length 2",
			want:    ContentError,
		},
		{
			name:    "error with FAIL",
			content: "FAIL compaction-mcp [build failed]",
			want:    ContentError,
		},
		{
			name:    "error with Traceback",
			content: "Traceback (most recent call last):\n  File \"main.py\", line 1",
			want:    ContentError,
		},
		{
			name:    "error with goroutine",
			content: "goroutine 1 [running]:\nmain.main()\n\t/app/main.go:12",
			want:    ContentError,
		},
		{
			name:    "code with backtick fence",
			content: "Here is the fix:\n```go\nfunc main() {}\n```",
			want:    ContentCode,
		},
		{
			name:    "code with func keyword",
			content: "func NewStore(budget int) *Store {\n\treturn &Store{}\n}",
			want:    ContentCode,
		},
		{
			name:    "code with import keyword",
			content: "import (\n\t\"fmt\"\n\t\"os\"\n)",
			want:    ContentCode,
		},
		{
			name:    "code with high bracket density",
			content: "{{{}}}(())[[]]{()}{{}}",
			want:    ContentCode,
		},
		{
			name:    "decision with decided",
			content: "We decided to use SQLite for local storage instead of BoltDB",
			want:    ContentDecision,
		},
		{
			name:    "decision with going with",
			content: "After discussion, going with the monorepo approach for simplicity",
			want:    ContentDecision,
		},
		{
			name:    "decision with approach:",
			content: "approach: sidecar proxy with Envoy for service mesh",
			want:    ContentDecision,
		},
		{
			name:    "tool output with dollar prompt",
			content: "$ go test -v ./...\nPASS\nok  \tcompaction-mcp\t0.003s",
			want:    ContentToolOutput,
		},
		{
			name:    "tool output with angle bracket prompt",
			content: "> ls -la /etc/nginx/\ntotal 48\ndrwxr-xr-x 2 root root 4096 Jan 1 00:00 conf.d",
			want:    ContentToolOutput,
		},
		{
			name:    "tool output with table chars",
			content: "Name       │ Status │ Age\n───────────├────────├─────\nnginx-pod  │ Running│ 2d",
			want:    ContentToolOutput,
		},
		{
			name:    "prose default",
			content: "The cilium project provides networking for Kubernetes clusters using eBPF technology.",
			want:    ContentProse,
		},
		{
			name:    "prose simple sentence",
			content: "Let's meet tomorrow to discuss the architecture.",
			want:    ContentProse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectContentType(tt.content)
			if got != tt.want {
				t.Errorf("DetectContentType() = %s, want %s",
					ContentTypeName(got), ContentTypeName(tt.want))
			}
		})
	}
}

func TestDetectPriority(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    ContentType
	}{
		{
			name:    "error beats code",
			content: "Error: compilation failed\nfunc main() {\n\tpanic(\"bad\")\n}",
			want:    ContentError,
		},
		{
			name:    "error beats decision",
			content: "We decided to fix the panic: runtime error in production",
			want:    ContentError,
		},
		{
			name:    "code beats decision",
			content: "We decided to use this:\nfunc Handle() {}",
			want:    ContentCode,
		},
		{
			name:    "code beats tool output",
			content: "$ cat main.go\npackage main\nimport \"fmt\"",
			want:    ContentToolOutput, // starts with "$ " so tool output wins first in priority? No -- Error > Code > Decision > ToolOutput
		},
	}

	// The last case: "$ " prefix makes it tool output, but "import " makes it code.
	// Priority is Error > Code > Decision > ToolOutput, so Code should win.
	// But "$ " prefix is checked in isToolOutput, and isCode is checked first.
	// Actually: DetectContentType checks error first, then code, then decision, then tool output.
	// "import " is in the content so isCode returns true => ContentCode.
	// Fix the expected value:
	tests[3].want = ContentCode

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectContentType(tt.content)
			if got != tt.want {
				t.Errorf("DetectContentType() = %s, want %s",
					ContentTypeName(got), ContentTypeName(tt.want))
			}
		})
	}
}

func TestAutoTags(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string // subset that must appear
	}{
		{
			name:    "go file paths produce go tag",
			content: "Modified /Users/dev/project/main.go and store.go to fix the issue",
			want:    []string{"go"},
		},
		{
			name:    "typescript file produces typescript tag",
			content: "Check the component in src/App.tsx for the bug",
			want:    []string{"react"},
		},
		{
			name:    "python file produces python tag",
			content: "Updated models.py with new schema",
			want:    []string{"python"},
		},
		{
			name:    "URL produces reference tag",
			content: "See https://kubernetes.io/docs/concepts/ for details",
			want:    []string{"reference", "kubernetes"},
		},
		{
			name:    "kubernetes keyword",
			content: "Deploy to Kubernetes cluster using helm chart",
			want:    []string{"kubernetes"},
		},
		{
			name:    "error content gets error tag",
			content: "Error: connection refused to redis server",
			want:    []string{"error", "redis"},
		},
		{
			name:    "docker keyword",
			content: "Build the Docker image with multi-stage builds",
			want:    []string{"docker"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AutoTags(tt.content)
			gotSet := make(map[string]struct{}, len(got))
			for _, tag := range got {
				gotSet[tag] = struct{}{}
			}
			for _, w := range tt.want {
				if _, ok := gotSet[w]; !ok {
					t.Errorf("AutoTags() missing expected tag %q, got %v", w, got)
				}
			}
		})
	}
}

func TestAutoTagsMax(t *testing.T) {
	// Content with many possible tags to verify the cap at 5.
	content := "Error: kubernetes docker cilium postgres nginx redis https://example.com main.go script.py app.tsx"
	tags := AutoTags(content)
	if len(tags) > maxTags {
		t.Errorf("AutoTags() returned %d tags, want at most %d: %v", len(tags), maxTags, tags)
	}
	// Should have some tags
	if len(tags) == 0 {
		t.Error("AutoTags() returned no tags for content-rich input")
	}
}

func TestScoreMultiplier(t *testing.T) {
	tests := []struct {
		ct   ContentType
		want float64
	}{
		{ContentError, 1.5},
		{ContentDecision, 1.3},
		{ContentCode, 1.2},
		{ContentProse, 1.0},
		{ContentToolOutput, 0.7},
	}

	for _, tt := range tests {
		t.Run(ContentTypeName(tt.ct), func(t *testing.T) {
			got := ScoreMultiplier(tt.ct)
			if got != tt.want {
				t.Errorf("ScoreMultiplier(%s) = %f, want %f",
					ContentTypeName(tt.ct), got, tt.want)
			}
		})
	}
}

func TestDecayHalfLife(t *testing.T) {
	tests := []struct {
		ct   ContentType
		want float64
	}{
		{ContentError, 30},
		{ContentDecision, 360},
		{ContentCode, 360},
		{ContentProse, 120},
		{ContentToolOutput, 15},
	}

	for _, tt := range tests {
		t.Run(ContentTypeName(tt.ct), func(t *testing.T) {
			got := DecayHalfLifeMinutes(tt.ct)
			if got != tt.want {
				t.Errorf("DecayHalfLifeMinutes(%s) = %f, want %f",
					ContentTypeName(tt.ct), got, tt.want)
			}
		})
	}
}

func TestContentTypeName(t *testing.T) {
	tests := []struct {
		want string
		ct   ContentType
	}{
		{"prose", ContentProse},
		{"code", ContentCode},
		{"error", ContentError},
		{"tool-output", ContentToolOutput},
		{"decision", ContentDecision},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := ContentTypeName(tt.ct)
			if got != tt.want {
				t.Errorf("ContentTypeName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEmptyContent(t *testing.T) {
	ct := DetectContentType("")
	if ct != ContentProse {
		t.Errorf("DetectContentType(\"\") = %s, want prose", ContentTypeName(ct))
	}

	tags := AutoTags("")
	if len(tags) != 0 {
		t.Errorf("AutoTags(\"\") = %v, want empty", tags)
	}

	// Also test whitespace-only
	ct = DetectContentType("   ")
	if ct != ContentProse {
		t.Errorf("DetectContentType(whitespace) = %s, want prose", ContentTypeName(ct))
	}

	tags = AutoTags(strings.Repeat(" ", 100))
	if len(tags) != 0 {
		t.Errorf("AutoTags(whitespace) = %v, want empty", tags)
	}
}
