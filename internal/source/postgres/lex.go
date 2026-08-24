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

// opensArguments reports whether t is the parenthesis that turns the name
// before it into a call.
func (t token) opensArguments() bool { return t.kind == tokenPunct && t.text == "(" }

// tokens splits sql the way postgres lexes it, as far as names go: every
// quoting form ends where postgres ends it, and every escape stands for the
// byte it means. Without that the deny list comes off with two characters —
// U&"pg_read_fil\0065"() is a call, and '--' in a literal blinds the rest (#71).
func tokens(sql string) []token {
	var out []token
	for i := 0; i < len(sql); {
		i, _ = separators(sql, i)
		if i >= len(sql) {
			break
		}
		r, w := utf8.DecodeRuneInString(sql[i:])
		switch {
		case isStringStart(sql, i):
			text, next := stringConstant(sql, i)
			out, i = append(out, token{tokenString, strings.ToLower(text)}), next
		case r == '"':
			text, next := quotedRun(sql, i, '"', false)
			out, i = append(out, token{tokenName, strings.ToLower(text)}), next
		case isUnicodeQuote(sql, i, '"'):
			text, next := unicodeQuoted(sql, i)
			out, i = append(out, token{tokenName, strings.ToLower(text)}), next
		case isDollarQuote(sql, i):
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

// separators skips whitespace and comments, and reports whether a newline was
// crossed — that is what makes two string constants one (see [stringConstant]).
func separators(sql string, i int) (int, bool) {
	newline := false
	for i < len(sql) {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				return len(sql), newline
			}
			i, newline = i+j+1, true
		case strings.HasPrefix(sql[i:], "/*"):
			n := skipBlockComment(sql[i:])
			newline = newline || strings.Contains(sql[i:i+n], "\n")
			i += n
		default:
			r, w := utf8.DecodeRuneInString(sql[i:])
			if !unicode.IsSpace(r) {
				return i, newline
			}
			newline, i = newline || r == '\n', i+w
		}
	}
	return i, newline
}

func isStringStart(sql string, i int) bool {
	return i < len(sql) && (sql[i] == '\'' || isPrefixedQuote(sql, i) || isUnicodeQuote(sql, i, '\''))
}

// stringConstant reads the string constant at i together with any postgres
// joins to it: two of them separated by whitespace holding a newline are one
// constant, so a name can be split across that seam.
func stringConstant(sql string, i int) (string, int) {
	var b strings.Builder
	for {
		text, next := stringRun(sql, i)
		b.WriteString(text)
		j, newline := separators(sql, next)
		if !newline || !isStringStart(sql, j) {
			return b.String(), next
		}
		i = j
	}
}

// stringRun is one constant of the '…' family, whatever prefix it carries.
func stringRun(sql string, i int) (string, int) {
	switch {
	case isUnicodeQuote(sql, i, '\''):
		return unicodeQuoted(sql, i)
	case isPrefixedQuote(sql, i):
		return quotedRun(sql, i+1, '\'', sql[i]|0x20 == 'e')
	default:
		return quotedRun(sql, i, '\'', false)
	}
}

// quotedRun reads the '…' or "…" run opening at i and returns what it holds:
// the delimiter doubled stands for itself, and a backslash escapes only in
// E'…' — standard_conforming_strings has been on by default since 9.1.
func quotedRun(sql string, i int, delim byte, escapes bool) (string, int) {
	var b strings.Builder
	for i++; i < len(sql); {
		switch {
		case escapes && sql[i] == '\\':
			text, next := escaped(sql, i)
			b.WriteString(text)
			i = next
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
	// An unterminated run ends here. Postgres refuses the statement, so the
	// guard does not have to: its own refusal would only be the vaguer one.
	return b.String(), len(sql)
}

// escaped decodes the backslash escape at i in an E'…' constant. The byte has
// to be decoded, not skipped: E'set_confi\x67(' is a call to set_config, which
// is the same disguise U&"…" wears (#71). An escape postgres does not define
// stands for the character itself.
func escaped(s string, i int) (string, int) {
	if i+1 >= len(s) {
		return `\`, len(s)
	}
	switch c := s[i+1]; {
	case c == 'x':
		if v, n := hexValue(s[i+2:], 2); n > 0 {
			return string([]byte{byte(v & 0xff)}), i + 2 + n
		}
	case c >= '0' && c <= '7':
		v, n := octValue(s[i+1:], 3)
		return string([]byte{byte(v & 0xff)}), i + 1 + n
	case c == 'u' || c == 'U':
		digits := 4
		if c == 'U' {
			digits = 8
		}
		if v, n := hexValue(s[i+2:], digits); n == digits {
			return string(v), i + 2 + n
		}
	case c == 'b':
		return "\b", i + 2
	case c == 'f':
		return "\f", i + 2
	case c == 'n':
		return "\n", i + 2
	case c == 'r':
		return "\r", i + 2
	case c == 't':
		return "\t", i + 2
	}
	return string(s[i+1]), i + 2
}

// isPrefixedQuote reports whether the byte at i is a literal prefix abutting
// its quote. Only E'…' takes escapes; B'…' and X'…' cannot spell a name at all,
// and are here so that their quote ends where postgres ends it.
func isPrefixedQuote(sql string, i int) bool {
	return i+1 < len(sql) && sql[i+1] == '\'' && strings.IndexByte("ebx", sql[i]|0x20) >= 0
}

func isUnicodeQuote(sql string, i int, delim byte) bool {
	return i+2 < len(sql) && sql[i]|0x20 == 'u' && sql[i+1] == '&' && sql[i+2] == delim
}

// unicodeQuoted reads U&"…" or U&'…' together with the UESCAPE clause that may
// follow it, and decodes the escapes: U&"pg_read_fil\0065" is a call to
// pg_read_file and the deny list has to see it as one. Postgres requires the
// prefix to abut the quote, so u&"x" is this form and not a bitwise and.
func unicodeQuoted(sql string, i int) (string, int) {
	text, next := quotedRun(sql, i+2, sql[i+2], false)
	esc, next := uescape(sql, next)
	return unescapeUnicode(text, esc), next
}

// uescape reads the optional UESCAPE 'c' clause, which replaces the backslash
// for the run just read. Anything that is not that clause leaves i where it was.
func uescape(sql string, i int) (byte, int) {
	j, _ := separators(sql, i)
	const kw = "uescape"
	if len(sql)-j < len(kw) || !strings.EqualFold(sql[j:j+len(kw)], kw) {
		return '\\', i
	}
	j += len(kw)
	if r, _ := utf8.DecodeRuneInString(sql[j:]); j < len(sql) && isIdentRune(r) {
		return '\\', i
	}
	j, _ = separators(sql, j)
	if j >= len(sql) || sql[j] != '\'' {
		return '\\', i
	}
	text, next := quotedRun(sql, j, '\'', false)
	if len(text) != 1 {
		return '\\', i
	}
	return text[0], next
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
		if v, n := hexValue(s[i+width-digits:], digits); n == digits {
			b.WriteRune(v)
			i += width
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// hexValue reads up to max hex digits and returns their value and how many it
// read; octValue does the same in base eight.
func hexValue(s string, max int) (rune, int) {
	var v rune
	n := 0
	for n < max && n < len(s) {
		var d rune
		switch c := s[n]; {
		case c >= '0' && c <= '9':
			d = rune(c - '0')
		case c|0x20 >= 'a' && c|0x20 <= 'f':
			d = rune(c|0x20-'a') + 10
		default:
			return v, n
		}
		v, n = v<<4|d, n+1
	}
	return v, n
}

func octValue(s string, max int) (rune, int) {
	var v rune
	n := 0
	for n < max && n < len(s) && s[n] >= '0' && s[n] <= '7' {
		v, n = v<<3|rune(s[n]-'0'), n+1
	}
	return v, n
}

// isDollarQuote reports whether the $ at i opens a $tag$…$tag$ constant. The tag
// follows the rules of an unquoted identifier minus the dollar sign, so $1 is a
// parameter and not a tag. A $ that continues an identifier never reaches here:
// it is an identifier rune, so the word scan has already taken it.
func isDollarQuote(sql string, i int) bool {
	if sql[i] != '$' {
		return false
	}
	_, end := dollarTag(sql, i)
	return end > 0
}

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
