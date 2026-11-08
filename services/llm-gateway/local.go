package main

import (
	"strings"
	"unicode"
)

func localComplete(system, last string) string {
	sys := strings.ToLower(system)
	last = strings.TrimSpace(last)
	switch {
	case strings.Contains(sys, "4 short bullet") || strings.Contains(sys, "summarize"):
		return localSummarize(last)
	case strings.Contains(sys, "rewrite"):
		return localRewrite(last)
	case strings.Contains(sys, "extract") && strings.Contains(sys, "task"):
		return localTasks(last)
	}
	if q, sources := parseRAGPrompt(last); q != "" {
		return localRAG(q, sources)
	}
	if strings.Contains(sys, "atlas agent") {
		if sources := retrievedContext(system); len(sources) > 0 {
			return "1. Read the matching note.\n2. Answer from it.\n\n" + localRAG(last, sources)
		}
		return "1. Look through stored notes.\n2. Nothing matched.\n\nPaste a note in Knowledge, then ask the agent again."
	}
	if sources := retrievedContext(system); len(sources) > 0 {
		return localRAG(last, sources)
	}
	return localFAQ(last)
}

func retrievedContext(system string) []string {
	const marker = "Retrieved context:"
	i := strings.Index(system, marker)
	if i < 0 {
		return nil
	}
	var sources []string
	for _, line := range strings.Split(system[i+len(marker):], "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if line != "" {
			sources = append(sources, line)
		}
	}
	return sources
}

func parseRAGPrompt(s string) (question string, sources []string) {
	const marker = "Question:"
	qi := strings.LastIndex(s, marker)
	if qi < 0 || !strings.Contains(s, "Sources:") {
		return "", nil
	}
	question = strings.TrimSpace(s[qi+len(marker):])
	head := s[:qi]
	srcIdx := strings.Index(head, "Sources:")
	if srcIdx < 0 {
		return question, nil
	}
	body := strings.TrimSpace(head[srcIdx+len("Sources:"):])
	if body == "" {
		return question, nil
	}
	for _, part := range strings.Split(body, "Source ") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		sources = append(sources, part)
	}
	return question, sources
}

func localRAG(question string, sources []string) string {
	if len(sources) == 0 {
		return "I don't have any of your stored documents for that yet. Paste a note in Knowledge, wait for it to embed, then ask again."
	}
	best := sources[0]
	bestScore := overlapScore(question, best)
	for _, s := range sources[1:] {
		if sc := overlapScore(question, s); sc > bestScore {
			best, bestScore = s, sc
		}
	}
	title, body := splitSource(best)
	var b strings.Builder
	b.WriteString("From your notes")
	if title != "" {
		b.WriteString(" (")
		b.WriteString(title)
		b.WriteString(")")
	}
	b.WriteString(":\n\n")
	b.WriteString(trimRunes(body, 700))
	if bestScore == 0 && len(sources) > 1 {
		b.WriteString("\n\nThat was the closest stored chunk; it may not fully answer “")
		b.WriteString(question)
		b.WriteString("”.")
	}
	return b.String()
}

func splitSource(s string) (title, body string) {
	// "1 (Retrieval):\ncontent"
	rest := s
	if i := strings.Index(rest, "("); i >= 0 {
		if j := strings.Index(rest[i:], "):"); j >= 0 {
			title = strings.TrimSpace(rest[i+1 : i+j])
			body = strings.TrimSpace(rest[i+j+2:])
			return title, body
		}
	}
	if i := strings.Index(s, ":\n"); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+2:])
	}
	if i := strings.Index(s, ": "); i > 0 && i < 80 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+2:])
	}
	return "", s
}

func overlapScore(q, src string) int {
	seen := map[string]struct{}{}
	score := 0
	for _, tok := range tokenize(strings.ToLower(src)) {
		seen[tok] = struct{}{}
	}
	for _, tok := range tokenize(strings.ToLower(q)) {
		if len(tok) < 3 {
			continue
		}
		if _, ok := seen[tok]; ok {
			score++
		}
	}
	return score
}

func localFAQ(q string) string {
	ql := strings.ToLower(q)
	switch {
	case containsAny(ql, "retriev", "pgvector", "embed", "chunk", "rag", "search"):
		return "Knowledge splits each document into chunks, embeds them (Voyage when keyed, otherwise a local bag-of-words vector), and stores them in Postgres with pgvector. Chat embeds your question and ranks chunks by cosine distance, then the model answers from those sources."
	case containsAny(ql, "remind", "notif", "nats", "email", "sms", "twilio", "ses"):
		return "Planner stores reminders with a due time. A background scan publishes atlas.notification.requested on NATS. The notification service delivers email (SES) or SMS (Twilio), or writes to the delivery log when running locally."
	case containsAny(ql, "account", "login", "jwt", "role", "admin", "auth"):
		return "The user service issues JWTs. The first registered account is admin. Tokens carry roles and permissions; knowledge, planner, and notify each check those claims independently."
	case containsAny(ql, "ingest", "document", "upload", "paste"):
		return "Sign in, open Knowledge, paste a title and body, then Store + embed. Planner also files an Inbox note when it sees atlas.knowledge.document.ingested on NATS."
	case containsAny(ql, "track", "note", "planner", "plan"):
		return "Planner keeps tracks (projects), notes, and reminders. Creating an account seeds an Inbox track. You can also paste prose into LLM assist to extract a track, note, or reminder."
	case containsAny(ql, "hello", "hi", "help", "what are you", "who are you"):
		return "I'm Atlas. Ask me anything. Attach a document to ground the answer with retrieval, or sign in and chat over notes you have ingested."
	default:
		return "I don't see a stored note that answers that. Paste one in Knowledge, then ask about it."
	}
}

func localSummarize(text string) string {
	sents := splitSentences(text)
	if len(sents) == 0 {
		return "Nothing to summarize."
	}
	n := 4
	if len(sents) < n {
		n = len(sents)
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("• ")
		b.WriteString(sents[i])
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func localRewrite(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "Nothing to rewrite."
	}
	if c := []rune(text); len(c) > 0 {
		c[0] = unicode.ToUpper(c[0])
		text = string(c)
	}
	if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "!") && !strings.HasSuffix(text, "?") {
		text += "."
	}
	return text
}

func localTasks(text string) string {
	var tasks []string
	for _, sent := range splitSentences(text) {
		low := strings.ToLower(sent)
		if containsAny(low, "then ", "ship ", "add ", "ingest ", "create ", "schedule ", "remind ", "send ", "build ", "fix ", "write ", "review ") {
			tasks = append(tasks, sent)
		}
	}
	if len(tasks) == 0 {
		for _, sent := range splitSentences(text) {
			tasks = append(tasks, sent)
			if len(tasks) == 5 {
				break
			}
		}
	}
	if len(tasks) == 0 {
		return "No actionable tasks found."
	}
	var b strings.Builder
	for i, t := range tasks {
		b.WriteString(itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func splitSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var out []string
	var b strings.Builder
	flush := func() {
		s := strings.TrimSpace(b.String())
		if s != "" {
			out = append(out, s)
		}
		b.Reset()
	}
	for _, r := range text {
		b.WriteRune(r)
		if r == '.' || r == '!' || r == '?' || r == '\n' {
			flush()
		}
	}
	flush()
	return out
}

func containsAny(s string, parts ...string) bool {
	for _, p := range parts {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
