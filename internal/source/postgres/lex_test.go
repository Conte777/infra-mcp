package postgres

import "testing"

// The lexer decides what both later checks even see, so the quoting forms are
// pinned here as tokens and in guard_test as refusals (#71).
func TestTokens(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []token
	}{
		{"words are folded", "SELECT Pg_Read_File", []token{{tokenWord, "select"}, {tokenWord, "pg_read_file"}}},
		{"punctuation stands alone", "a.b(", []token{
			{tokenWord, "a"}, {tokenPunct, "."}, {tokenWord, "b"}, {tokenPunct, "("},
		}},
		{"line comment", "select 1 -- pg_read_file\nfrom t", []token{
			{tokenWord, "select"}, {tokenWord, "1"}, {tokenWord, "from"}, {tokenWord, "t"},
		}},
		{"nested block comment", "select/* a /* b */ c */1", []token{{tokenWord, "select"}, {tokenWord, "1"}}},
		{"unterminated block comment", "select/* forever", []token{{tokenWord, "select"}}},
		{"a comment inside a name splits it", "select pg_read/**/_file", []token{
			{tokenWord, "select"}, {tokenWord, "pg_read"}, {tokenWord, "_file"},
		}},
		{"quoted identifier", `select "pg_read_file"`, []token{{tokenWord, "select"}, {tokenName, "pg_read_file"}}},
		{"a doubled quote is the quote", `select "a""b"`, []token{{tokenWord, "select"}, {tokenName, `a"b`}}},
		{"a comment marker inside a literal is data", "select '--'", []token{
			{tokenWord, "select"}, {tokenString, "--"},
		}},
		{"a doubled apostrophe is the apostrophe", "select 'a''b'", []token{
			{tokenWord, "select"}, {tokenString, "a'b"},
		}},
		{"an escaped apostrophe ends nothing", `select E'\'' , 1`, []token{
			{tokenWord, "select"}, {tokenString, "'"}, {tokenPunct, ","}, {tokenWord, "1"},
		}},
		{"an escape stands for the byte after it", `select E'a\bc'`, []token{
			{tokenWord, "select"}, {tokenString, "abc"},
		}},
		{"bit and hex strings", "select b'10', x'ff'", []token{
			{tokenWord, "select"}, {tokenString, "10"}, {tokenPunct, ","}, {tokenString, "ff"},
		}},
		{"dollar quoting", "select $$--$$", []token{{tokenWord, "select"}, {tokenString, "--"}}},
		{"a tagged dollar quote", "select $tag$a$$b$tag$", []token{
			{tokenWord, "select"}, {tokenString, "a$$b"},
		}},
		// A dollar sign continues an identifier, so postgres reads no quote here
		// either — and the COPY lock leans on that.
		{"a dollar quote cannot follow an identifier", "program$$x$$", []token{{tokenWord, "program$$x$$"}}},
		{"a parameter is not a tag", "select $1", []token{{tokenWord, "select"}, {tokenWord, "$1"}}},
		{"unicode escapes are decoded", `select U&"pg_read_fil\0065"`, []token{
			{tokenWord, "select"}, {tokenName, "pg_read_file"},
		}},
		{"the six digit form", `select U&"\+000041"`, []token{{tokenWord, "select"}, {tokenName, "a"}}},
		{"uescape replaces the escape character", `select U&"a!0062" UESCAPE '!'`, []token{
			{tokenWord, "select"}, {tokenName, "ab"},
		}},
		{"uescape reached past a comment", `select U&"a!0062"/**/UESCAPE'!'`, []token{
			{tokenWord, "select"}, {tokenName, "ab"},
		}},
		{"a doubled escape is the escape", `select U&"a\\b"`, []token{
			{tokenWord, "select"}, {tokenName, `a\b`},
		}},
		{"a broken escape is passed through", `select U&"pg_read_fil\zzzz"`, []token{
			{tokenWord, "select"}, {tokenName, `pg_read_fil\zzzz`},
		}},
		{"uescape that is not the clause is left alone", `select U&"a\0062" UESCAPED`, []token{
			{tokenWord, "select"}, {tokenName, "ab"}, {tokenWord, "uescaped"},
		}},
		{"the string form takes escapes too", `select U&'a\0062'`, []token{
			{tokenWord, "select"}, {tokenString, "ab"},
		}},
		{"an identifier with no escapes at all", `select U&"abc"`, []token{
			{tokenWord, "select"}, {tokenName, "abc"},
		}},
		{"a truncated escape", `select U&"a\006"`, []token{{tokenWord, "select"}, {tokenName, `a\006`}}},
		{"uescape without a literal", `select U&"a\0062" UESCAPE x`, []token{
			{tokenWord, "select"}, {tokenName, "ab"}, {tokenWord, "uescape"}, {tokenWord, "x"},
		}},
		{"uescape with more than one character", `select U&"a\0062" UESCAPE 'xy'`, []token{
			{tokenWord, "select"}, {tokenName, "ab"}, {tokenWord, "uescape"}, {tokenString, "xy"},
		}},
		{"uescape reached past a line comment", "select U&\"a!0062\"--c\nUESCAPE'!'", []token{
			{tokenWord, "select"}, {tokenName, "ab"},
		}},
		{"a line comment where uescape would be", "select U&\"a\\0062\"--c", []token{
			{tokenWord, "select"}, {tokenName, "ab"},
		}},
		{"a dollar tag that never closes", "select $abc", []token{
			{tokenWord, "select"}, {tokenWord, "$abc"},
		}},
		{"unterminated literal", "select 'abc", []token{{tokenWord, "select"}, {tokenString, "abc"}}},
		{"unterminated dollar quote", "select $$abc", []token{{tokenWord, "select"}, {tokenString, "abc"}}},
		{"unterminated quoted identifier", `select "abc`, []token{{tokenWord, "select"}, {tokenName, "abc"}}},
		{"empty", "  \n\t ", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokens(tt.sql)
			if len(got) != len(tt.want) {
				t.Fatalf("tokens(%q) = %v, want %v", tt.sql, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("tokens(%q) = %v, want %v", tt.sql, got, tt.want)
				}
			}
		})
	}
}
