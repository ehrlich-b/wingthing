package skill

import (
	"regexp"
	"strings"
)

var markerRe = regexp.MustCompile(`\{\{([^}]+)\}\}`)

type InterpolateData struct {
	Memory  map[string]string // memory file name -> body content
	Identity map[string]string // identity frontmatter field -> value
	Thread  string            // rendered thread summary
	Task    string            // task.what value
}

type Warning struct {
	Marker  string
	Message string
}

func Interpolate(body string, data InterpolateData) (string, []Warning) {
	var warnings []Warning

	result := markerRe.ReplaceAllStringFunc(body, func(match string) string {
		// Strip {{ and }}
		inner := match[2 : len(match)-2]
		parts := strings.SplitN(inner, ".", 2)
		if len(parts) != 2 {
			return match // unrecognized, leave as-is
		}

		ns, key := parts[0], parts[1]
		switch ns {
		case "memory":
			if v, ok := data.Memory[key]; ok {
				return v
			}
			warnings = append(warnings, Warning{Marker: match, Message: "memory file not found: " + key})
			return ""
		case "identity":
			if v, ok := data.Identity[key]; ok {
				return v
			}
			warnings = append(warnings, Warning{Marker: match, Message: "identity field not found: " + key})
			return ""
		case "thread":
			if key == "summary" {
				return data.Thread
			}
			return match // unrecognized thread key, leave as-is
		case "task":
			if key == "what" {
				return data.Task
			}
			return match // unrecognized task key, leave as-is
		default:
			return match // unrecognized namespace, leave as-is
		}
	})

	return result, warnings
}
