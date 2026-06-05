package db

import "strings"

// rebindQuery rewrites SQLite-style `?` bind placeholders into Postgres-style
// `$1, $2, ...`. A `?` inside a single-quoted SQL string literal is left
// untouched; SQL escapes a quote inside a literal by doubling it (”) which the
// scanner treats as a quote that stays inside the literal, so an apostrophe in
// text never ends the literal early.
func rebindQuery(query string) string {
	if !strings.ContainsRune(query, '?') {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	inLiteral := false
	n := 0
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'':
			inLiteral = !inLiteral
			b.WriteByte(c)
		case c == '?' && !inLiteral:
			n++
			b.WriteByte('$')
			b.WriteString(itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// itoa avoids strconv for the hot path; placeholder counts are small.
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
