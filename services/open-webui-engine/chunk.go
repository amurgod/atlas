package main

import (
	"strings"
	"unicode"
)

func chunkText(text string, max int) []string {
	paras := strings.Split(text, "\n")
	var chunks []string
	var buf strings.Builder
	flush := func() {
		s := strings.TrimSpace(buf.String())
		if s != "" {
			chunks = append(chunks, s)
		}
		buf.Reset()
	}
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if buf.Len()+len(p)+1 > max && buf.Len() > 0 {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		if len(p) <= max {
			buf.WriteString(p)
			continue
		}
		for _, sent := range splitRunes(p, max) {
			if buf.Len()+len(sent)+1 > max && buf.Len() > 0 {
				flush()
			}
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(sent)
		}
	}
	flush()
	if len(chunks) == 0 {
		return []string{strings.TrimSpace(text)}
	}
	return chunks
}

func splitRunes(s string, max int) []string {
	if len(s) <= max {
		return []string{s}
	}
	var out []string
	for len(s) > max {
		cut := max
		for cut > max/2 && cut < len(s) && !unicode.IsSpace(rune(s[cut-1])) {
			cut--
		}
		out = append(out, strings.TrimSpace(s[:cut]))
		s = strings.TrimSpace(s[cut:])
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
