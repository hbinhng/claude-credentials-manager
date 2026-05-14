package shellalias

import (
	"bytes"
	"strings"
)

// aliasBlock is one ccm-managed alias inside an alias file.
type aliasBlock struct {
	Name string
	Body string // function definition between the sentinel lines (no trailing newline)
}

const (
	aliasBeginPrefix = "# ccm-alias:begin:"
	aliasEndPrefix   = "# ccm-alias:end:"
)

// parseAliasBlocks extracts every ccm-managed block from an alias file.
// Order matches file order. Lines outside begin/end fences are ignored.
func parseAliasBlocks(content []byte) []aliasBlock {
	if len(content) == 0 {
		return nil
	}
	var out []aliasBlock
	lines := strings.Split(string(content), "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, aliasBeginPrefix) {
			name := strings.TrimPrefix(line, aliasBeginPrefix)
			j := i + 1
			var body []string
			endLine := aliasEndPrefix + name
			for j < len(lines) && lines[j] != endLine {
				body = append(body, lines[j])
				j++
			}
			if j < len(lines) {
				out = append(out, aliasBlock{Name: name, Body: strings.Join(body, "\n")})
				i = j + 1
				continue
			}
		}
		i++
	}
	return out
}

// upsertAliasBlock returns a new buffer that contains the given alias.
// If a block with this name exists, its body is replaced in-place;
// otherwise the block is appended to the end. The output always ends
// in a newline.
func upsertAliasBlock(content []byte, name, body string) []byte {
	newBlock := aliasBeginPrefix + name + "\n" + body + "\n" + aliasEndPrefix + name + "\n"
	beginLine := aliasBeginPrefix + name
	endLine := aliasEndPrefix + name

	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		if line == beginLine {
			for j := i + 1; j < len(lines); j++ {
				if lines[j] == endLine {
					before := strings.Join(lines[:i], "\n")
					after := strings.Join(lines[j+1:], "\n")
					var b bytes.Buffer
					if before != "" {
						b.WriteString(before)
						if !strings.HasSuffix(before, "\n") {
							b.WriteByte('\n')
						}
					}
					b.WriteString(newBlock)
					if after != "" && after != "\n" {
						b.WriteString(strings.TrimPrefix(after, "\n"))
					}
					return b.Bytes()
				}
			}
			break // unterminated begin; treat as missing
		}
	}

	// Append.
	var b bytes.Buffer
	if len(content) > 0 {
		b.Write(content)
		if !bytes.HasSuffix(content, []byte("\n")) {
			b.WriteByte('\n')
		}
	}
	b.WriteString(newBlock)
	return b.Bytes()
}

// removeAliasBlock returns the buffer with the named block removed.
// `ok` is false if no such block was found.
func removeAliasBlock(content []byte, name string) ([]byte, bool) {
	beginLine := aliasBeginPrefix + name
	endLine := aliasEndPrefix + name
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		if line == beginLine {
			for j := i + 1; j < len(lines); j++ {
				if lines[j] == endLine {
					before := strings.Join(lines[:i], "\n")
					after := strings.Join(lines[j+1:], "\n")
					var b bytes.Buffer
					if before != "" {
						b.WriteString(before)
						if !strings.HasSuffix(before, "\n") {
							b.WriteByte('\n')
						}
					}
					if after != "" && after != "\n" {
						b.WriteString(strings.TrimPrefix(after, "\n"))
					}
					return b.Bytes(), true
				}
			}
		}
	}
	return content, false
}
