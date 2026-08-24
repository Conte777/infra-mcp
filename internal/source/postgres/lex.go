package postgres

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokenKind is as much of postgres' lexical grammar as the deny list needs: it
// separates a name a statement calls from a name it merely spells inside a
// string. Nothing above the token level is read — the list matches a name, it
// does not parse SQL (ADR-0001).
type tokenKind uint8

const (
	tokenWord   tokenKind = iota // unquoted identifier or keyword
	tokenName                    // quoted identifier: "…" or U&"…"
	tokenString                  // string constant, in any of its quoting forms
	tokenPunct                   // one rune of everything else
)

// text is folded to lower case for every kind but punctuation, so the deny list
// has one rule. A quoted identifier is case-sensitive in postgres, so folding
// it can only over-match a name that does not exist.
type token struct {
	kind tokenKind
	text string
}

// tokens splits sql the way postgres lexes it, as far as names go: every
// quoting form ends where postgres ends it. Without that the deny list comes off
// with two characters — U&"pg_read_fil\0065"() is a call, and a '--' inside a
// literal blinds the rest of the statement (#71). What the lexer cannot finish
// is passed through, not refused: postgres will not run it either.
func tokens(sql string) []token {
	var out []token
	for i := 0; i < len(sql); {
		if strings.HasPrefix(sql[i:], "--") {
			if j := strings.IndexByte(sql[i:], '\n'); j >= 0 {
				i += j + 1
			} else {
				i = len(sql)
			}
			continue
		}
		if strings.HasPrefix(sql[i:], "/*") {
			i += skipBlockComment(sql[i:])
			continue
		}
		r, w := utf8.DecodeRuneInString(sql[i:])
		switch {
		case unicode.IsSpace(r):
			i += w
		case r == '\'':
			text, next := quotedRun(sql, i, '\'', false)
			out, i = append(out, token{tokenString, strings.ToLower(text)}), next
		case r == '"':
			text, next := quotedRun(sql, i, '"', false)
			out, i = append(out, token{tokenName, strings.ToLower(text)}), next
		case isUnicodeQuote(sql, i):
			text, kind, next := unicodeQuoted(sql, i)
			out, i = append(out, token{kind, strings.ToLower(text)}), next
		// E'…' takes backslash escapes; B'…' and X'…' take none, but they end
		// like any other literal, and a bit string cannot spell a name anyway.
		case isPrefixedQuote(sql, i, "ebx"):
			text, next := quotedRun(sql, i+1, '\'', sql[i]|0x20 == 'e')
			out, i = append(out, token{tokenString, strings.ToLower(text)}), next
		case r == '$' && isDollarQuote(sql, i):
			text, next := dollarRun(sql, i)
			out, i = append(out, token{tokenString, strings.ToLower(text)}), next
		case isIdentRune(r):
			end := i + w
			for end < len(sql) {
				r, w := utf8.DecodeRuneInString(sql[end:])
				if !isIdentRune(r) {
					break
				}
				end += w
			}
			out, i = append(out, token{tokenWord, strings.ToLower(sql[i:end])}), end
		default:
			out, i = append(out, token{tokenPunct, sql[i : i+w]}), i+w
		}
	}
	return out
}

// quotedRun reads the '…' or "…" run opening at i and returns what it holds:
// the delimiter doubled stands for itself, and backslash escapes the next byte
// only in E'…' — standard_conforming_strings has been on by default since 9.1,
// so a plain literal has no backslash escapes. The bytes an escape stands for do
// not matter here, only where the run ends.
func quotedRun(sql string, i int, delim byte, backslash bool) (string, int) {
	var b strings.Builder
	for i++; i < len(sql); {
		switch {
		case backslash && sql[i] == '\\' && i+1 < len(sql):
			b.WriteByte(sql[i+1])
			i += 2
		case sql[i] == delim && i+1 < len(sql) && sql[i+1] == delim:
			b.WriteByte(delim)
			i += 2
		case sql[i] == delim:
			return b.String(), i + 1
		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	return b.String(), len(sql)
}

// isPrefixedQuote reports whether sql[i] is one of the given literal prefixes
// followed immediately by a quote, as in E'…'. The prefix is one byte, so it is
// compared with the case bit cleared.
func isPrefixedQuote(sql string, i int, prefixes string) bool {
	return i+1 < len(sql) && sql[i+1] == '\'' && strings.IndexByte(prefixes, sql[i]|0x20) >= 0
}

func isUnicodeQuote(sql string, i int) bool {
	return i+2 < len(sql) && sql[i]|0x20 == 'u' && sql[i+1] == '&' &&
		(sql[i+2] == '"' || sql[i+2] == '\'')
}

// unicodeQuoted reads U&"…" or U&'…' together with the UESCAPE clause that may
// follow it, and decodes the escapes: U&"pg_read_fil\0065" is a call to
// pg_read_file and the deny list has to see it as one. Postgres requires the
// prefix to abut the quote, so u&"x" is this form and not a bitwise and.
func unicodeQuoted(sql string, i int) (string, tokenKind, int) {
	delim := sql[i+2]
	text, next := quotedRun(sql, i+2, delim, false)
	esc, next := uescape(sql, next)
	kind := tokenName
	if delim == '\'' {
		kind = tokenString
	}
	return unescapeUnicode(text, esc), kind, next
}

// uescape reads the optional UESCAPE 'c' clause, which replaces the backslash
// for the run just read. Anything that is not that clause leaves i where it was.
func uescape(sql string, i int) (byte, int) {
	j := skipSeparators(sql, i)
	const kw = "uescape"
	if len(sql)-j < len(kw) || !strings.EqualFold(sql[j:j+len(kw)], kw) {
		return '\\', i
	}
	j += len(kw)
	if r, _ := utf8.DecodeRuneInString(sql[j:]); j < len(sql) && isIdentRune(r) {
		return '\\', i
	}
	j = skipSeparators(sql, j)
	if j >= len(sql) || sql[j] != '\'' {
		return '\\', i
	}
	text, next := quotedRun(sql, j, '\'', false)
	if len(text) != 1 {
		return '\\', i
	}
	return text[0], next
}

// skipSeparators is the whitespace and comments postgres allows between a
// U&"…" run and its UESCAPE clause.
func skipSeparators(sql string, i int) int {
	for i < len(sql) {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			if j := strings.IndexByte(sql[i:], '\n'); j >= 0 {
				i += j + 1
			} else {
				return len(sql)
			}
		case strings.HasPrefix(sql[i:], "/*"):
			i += skipBlockComment(sql[i:])
		default:
			r, w := utf8.DecodeRuneInString(sql[i:])
			if !unicode.IsSpace(r) {
				return i
			}
			i += w
		}
	}
	return i
}

// unescapeUnicode decodes \XXXX, \+XXXXXX and the doubled escape character. An
// escape that does not parse is written out as it stands: postgres refuses the
// statement, and refusing it here first would only replace its message with a
// worse one (#71).
func unescapeUnicode(s string, esc byte) string {
	if strings.IndexByte(s, esc) < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != esc {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == esc {
			b.WriteByte(esc)
			i += 2
			continue
		}
		digits, width := 4, 5
		if i+1 < len(s) && s[i+1] == '+' {
			digits, width = 6, 8
		}
		if r, ok := hexRune(s[i+width-digits:], digits); ok {
			b.WriteRune(r)
			i += width
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func hexRune(s string, digits int) (rune, bool) {
	if len(s) < digits {
		return 0, false
	}
	var v rune
	for i := 0; i < digits; i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			v = v<<4 | rune(c-'0')
		case c|0x20 >= 'a' && c|0x20 <= 'f':
			v = v<<4 | rune(c|0x20-'a'+10)
		default:
			return 0, false
		}
	}
	return v, true
}

// isDollarQuote reports whether the $ at i opens a $tag$…$tag$ constant. The tag
// follows the rules of an unquoted identifier minus the dollar sign, so $1 is a
// parameter and not a tag. A $ that continues an identifier never reaches here:
// it is an identifier rune, so the word scan has already taken it.
func isDollarQuote(sql string, i int) bool {
	_, end := dollarTag(sql, i)
	return end > 0
}

// dollarTag is the $tag$ delimiter opening at i, and where it ends.
func dollarTag(sql string, i int) (string, int) {
	j := i + 1
	for j < len(sql) {
		r, w := utf8.DecodeRuneInString(sql[j:])
		if r == '$' || !isIdentRune(r) {
			break
		}
		if j == i+1 && unicode.IsDigit(r) {
			return "", 0
		}
		j += w
	}
	if j >= len(sql) || sql[j] != '$' {
		return "", 0
	}
	return sql[i : j+1], j + 1
}

func dollarRun(sql string, i int) (string, int) {
	delim, start := dollarTag(sql, i)
	body := sql[start:]
	end := strings.Index(body, delim)
	if end < 0 {
		return body, len(sql)
	}
	return body[:end], start + end + len(delim)
}

// skipBlockComment is the length of the comment starting at s. Postgres nests
// block comments, so the closing marker is found by counting, not by searching.
func skipBlockComment(s string) int {
	depth, i := 1, 2
	for depth > 0 && i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(s[i:], "*/"):
			depth--
			i += 2
		default:
			i++
		}
	}
	return min(i, len(s))
}

func isIdentRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
