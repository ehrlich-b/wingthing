package agent

import "strings"

const maxShapeTail = 256

type tokenKind uint8

const (
	keyToken tokenKind = iota + 1
	githubToken
	slackToken
	jwtToken
	bearerShape
	bearerWhitespace
)

var tokenShapes = []struct {
	prefix string
	kind   tokenKind
	min    int
}{
	{"sk-", keyToken, 8},
	{"ghp_", githubToken, 8}, {"gho_", githubToken, 8}, {"ghu_", githubToken, 8}, {"ghs_", githubToken, 8}, {"ghr_", githubToken, 8},
	{"github_pat_", githubToken, 8},
	{"xoxb-", slackToken, 8}, {"xoxa-", slackToken, 8}, {"xoxp-", slackToken, 8}, {"xoxr-", slackToken, 8}, {"xoxs-", slackToken, 8},
	{"AIza", keyToken, 20}, {"eyJ", jwtToken, 1}, {"Bearer", bearerShape, 1},
}

// Shape matching shares the known-value stream boundary. A possible token is
// held until its delimiter or EOF. Very long candidates are conservatively
// redacted after a fixed window, and their continuation is discarded until the
// delimiter, keeping memory bounded even for arbitrarily long tokens/JWTs.
type shapeRedaction struct {
	pending string
	discard tokenKind
	word    bool
}

func tokenWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

func tokenBody(c byte, kind tokenKind) bool {
	if tokenWord(c) {
		return c != '_' || kind != slackToken
	}
	if c == '-' {
		return kind != githubToken
	}
	if kind == jwtToken {
		return c == '.'
	}
	return kind == bearerShape && strings.ContainsRune(".~+/=", rune(c))
}

func shapeCandidate(text string, word bool) (end, label int, kind tokenKind, valid, partial bool) {
	for _, shape := range tokenShapes {
		if word && shape.kind != bearerShape {
			continue
		}
		n := min(len(text), len(shape.prefix))
		matches := text[:n] == shape.prefix[:n]
		if shape.kind == bearerShape {
			matches = strings.EqualFold(text[:n], shape.prefix[:n])
		}
		if !matches {
			continue
		}
		if n < len(shape.prefix) {
			return len(text), 0, shape.kind, false, true
		}
		start := n
		if shape.kind == bearerShape {
			for start < len(text) && (text[start] == ' ' || text[start] == '\t') {
				start++
			}
			if start == len(text) {
				return start, start, shape.kind, false, true
			}
			if start == n {
				continue
			}
			label = start
		}
		end = start
		for end < len(text) && tokenBody(text[end], shape.kind) {
			end++
		}
		valid = end-start >= shape.min
		if shape.kind == jwtToken {
			parts := strings.Split(text[:end], ".")
			valid = len(parts) >= 3 && len(parts[0]) > 3 && parts[1] != "" && parts[2] != ""
		}
		return end, label, shape.kind, valid, end == len(text)
	}
	return 0, 0, 0, false, false
}

func (s *shapeRedaction) write(text string) string {
	text = s.pending + text
	s.pending = ""
	var output strings.Builder
	for text != "" {
		if s.discard == bearerWhitespace {
			if text[0] == ' ' || text[0] == '\t' {
				output.WriteByte(text[0])
				s.word = false
				text = text[1:]
				continue
			}
			s.discard = 0
			if tokenBody(text[0], bearerShape) {
				output.WriteString("[redacted]")
				s.discard = bearerShape
			}
		}
		if s.discard != 0 {
			end := 0
			for end < len(text) && tokenBody(text[end], s.discard) {
				end++
			}
			if end > 0 {
				s.word = tokenWord(text[end-1])
			}
			text = text[end:]
			if text == "" {
				break
			}
			s.discard = 0
		}
		end, label, kind, valid, partial := 0, 0, tokenKind(0), false, false
		if strings.ContainsRune("sgxAebB", rune(text[0])) {
			end, label, kind, valid, partial = shapeCandidate(text, s.word)
		}
		if partial && len(text) < maxShapeTail {
			s.pending = strings.Clone(text)
			break
		}
		if partial && kind == bearerShape && label == len(text) {
			output.WriteString(text)
			s.word = false
			s.discard = bearerWhitespace
			break
		}
		if valid || partial {
			output.WriteString(text[:label])
			output.WriteString("[redacted]")
			s.word = tokenWord(text[end-1])
			text = text[end:]
			if partial {
				s.discard = kind
			}
		} else {
			output.WriteByte(text[0])
			s.word = tokenWord(text[0])
			text = text[1:]
		}
	}
	return output.String()
}

func (s *shapeRedaction) finish() string {
	text := s.pending
	s.pending = ""
	if text == "" {
		return ""
	}
	end, label, _, valid, _ := shapeCandidate(text, s.word)
	if valid {
		return text[:label] + "[redacted]" + text[end:]
	}
	return text
}
