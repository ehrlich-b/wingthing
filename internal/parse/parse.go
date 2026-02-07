package parse

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Warning struct {
	Message string
}

type Result struct {
	Schedules []ScheduleDirective
	Memories  []MemoryDirective
	Warnings  []Warning
}

var (
	scheduleRe = regexp.MustCompile(`(?s)<!--\s*wt:schedule\s+(.*?)-->(.*?)<!--\s*/wt:schedule\s*-->`)
	memoryRe   = regexp.MustCompile(`(?s)<!--\s*wt:memory\s+(.*?)-->(.*?)<!--\s*/wt:memory\s*-->`)
	attrRe     = regexp.MustCompile(`(\w+)=("([^"]*?)"|(\S+))`)
)

func Parse(output string) Result {
	var r Result

	for _, m := range scheduleRe.FindAllStringSubmatch(output, -1) {
		attrs := parseAttrs(m[1])
		content := strings.TrimSpace(m[2])
		if content == "" {
			r.Warnings = append(r.Warnings, Warning{Message: "wt:schedule with empty content, skipping"})
			continue
		}

		sd := ScheduleDirective{Content: content}
		hasDelay := attrs["delay"] != ""
		hasAt := attrs["at"] != ""

		if !hasDelay && !hasAt {
			r.Warnings = append(r.Warnings, Warning{Message: fmt.Sprintf("wt:schedule missing delay or at attribute, skipping: %q", content)})
			continue
		}

		if hasDelay {
			d, err := time.ParseDuration(attrs["delay"])
			if err != nil {
				r.Warnings = append(r.Warnings, Warning{Message: fmt.Sprintf("wt:schedule invalid delay %q: %v", attrs["delay"], err)})
				continue
			}
			if d > maxDelay {
				d = maxDelay
				r.Warnings = append(r.Warnings, Warning{Message: fmt.Sprintf("wt:schedule delay exceeds 24h cap, clamped to 24h for: %q", content)})
			}
			sd.Delay = d
		}

		if hasAt {
			t, err := time.Parse(time.RFC3339, attrs["at"])
			if err != nil {
				r.Warnings = append(r.Warnings, Warning{Message: fmt.Sprintf("wt:schedule invalid at %q: %v", attrs["at"], err)})
				continue
			}
			sd.At = t
		}

		if mem := attrs["memory"]; mem != "" {
			for _, f := range strings.Split(mem, ",") {
				f = strings.TrimSpace(f)
				if f != "" {
					sd.Memory = append(sd.Memory, f)
				}
			}
		}

		if after := attrs["after"]; after != "" {
			sd.After = after
		}

		r.Schedules = append(r.Schedules, sd)
	}

	for _, m := range memoryRe.FindAllStringSubmatch(output, -1) {
		attrs := parseAttrs(m[1])
		content := strings.TrimSpace(m[2])

		file := attrs["file"]
		if file == "" {
			r.Warnings = append(r.Warnings, Warning{Message: "wt:memory missing file attribute, skipping"})
			continue
		}
		if content == "" {
			r.Warnings = append(r.Warnings, Warning{Message: fmt.Sprintf("wt:memory with empty content for file %q, skipping", file)})
			continue
		}

		r.Memories = append(r.Memories, MemoryDirective{File: file, Content: content})
	}

	return r
}

func parseAttrs(s string) map[string]string {
	attrs := map[string]string{}
	for _, m := range attrRe.FindAllStringSubmatch(s, -1) {
		key := m[1]
		if m[3] != "" {
			attrs[key] = m[3]
		} else {
			attrs[key] = m[4]
		}
	}
	return attrs
}
