// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the GO-LICENSE file.

// The original file is at:
//
//	https://github.com/golang/go/blob/master/src/go/scanner/scanner_test.go

// Modification Copyright 2022 The Gc Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gc // import "modernc.org/gc/v3"

import (
	"fmt"
	"go/token"
	"strings"
	"testing"
)

func testScanErrors(t *testing.T) {
	for itest, test := range []struct {
		src string
		ch  token.Token
		off int
		_   string // not used
		err string
	}{
		{"'\n   ", CHAR, 1, "'", "non-printable character: U+000A"},
		{"'\n", CHAR, 1, "'", "non-printable character: U+000A"},
		{"'\ufeff" + `'`, CHAR, 1, "'\ufeff" + `'`, "illegal byte order mark"}, // only first BOM is ignored
		{"'n   ", CHAR, 2, "'", "rune literal not terminated"},
		{"'n", CHAR, 2, "'", "rune literal not terminated"},
		{"..", PERIOD, 0, "", ""}, // two periods, not invalid token (issue #28112)
		{"/*", ILLEGAL, 0, "/*", "comment not terminated"},
		{"/**/", EOF, 0, "/**/", ""},
		{"//\ufeff", EOF, 2, "//\ufeff", "illegal byte order mark"}, // only first BOM is ignored
		{"07090000008", INT, 3, "07090000008", "invalid digit '9' in octal literal"},
		{"077", INT, 0, "077", ""},
		{"078", INT, 2, "078", "invalid digit '8' in octal literal"},
		{"078.", FLOAT, 0, "078.", ""},
		{"07801234567.", FLOAT, 0, "07801234567.", ""},
		{"078e0", FLOAT, 0, "078e0", ""},
		{"0E", FLOAT, 2, "0E", "exponent has no digits"}, // issue 17621
		{"0x", INT, 2, "0x", "hexadecimal literal has no digits"},
		{"\"abc\n   ", token.STRING, 0, `"abc`, "string literal not terminated"},
		{"\"abc\n", token.STRING, 0, `"abc`, "string literal not terminated"},
		{"\"abc\x00def\"", STRING, 4, "\"abc\x00def\"", "illegal character NUL"},
		{"\"abc\x80def\"", STRING, 4, "\"abc\x80def\"", "illegal UTF-8 encoding"},
		{"\"abcn   ", STRING, 0, `"abc`, "string literal not terminated"},
		{"\"abcn", STRING, 0, `"abc`, "string literal not terminated"},
		{"\a", ILLEGAL, 0, "", "illegal character U+0007"},
		{"\a", ILLEGAL, 0, "", "illegal character U+0007"},
		{"\ufeff\ufeff", ILLEGAL, 3, "\ufeff\ufeff", "illegal byte order mark"}, // only first BOM is ignored
		{"\ufeff\ufeff", ILLEGAL, 3, "\ufeff\ufeff", "illegal byte order mark"}, // only first BOM is ignored
		{"`", STRING, 1, "`", "raw string literal not terminated"},
		{"``", STRING, 0, "``", ""},
		{"abc\x00", IDENT, 3, "abc", "illegal character NUL"},
		{"abc\x00def", IDENT, 3, "abc", "illegal character NUL"},
		{`""`, STRING, 0, `""`, ""},
		{`"` + "abc\ufeffdef" + `"`, STRING, 4, `"` + "abc\ufeffdef" + `"`, "illegal byte order mark"}, // only first BOM is ignored
		{`"abc`, STRING, 0, `"abc`, "string literal not terminated"},
		{`#`, ILLEGAL, 0, "", "illegal character U+0023 '#'"},
		{`#`, ILLEGAL, 0, "", "illegal character U+0023 '#'"},
		{`' '`, CHAR, 0, `' '`, ""},
		{`''`, CHAR, 0, `''`, "illegal rune literal"},
		{`'12'`, CHAR, 0, `'12'`, "illegal rune literal"},
		{`'123'`, CHAR, 0, `'123'`, "illegal rune literal"},
		{`'\0'`, CHAR, 3, `'\0'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\07'`, CHAR, 4, `'\07'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\08'`, CHAR, 3, `'\08'`, "illegal character U+0038 '8' in escape sequence"},
		{`'\8'`, CHAR, 2, `'\8'`, "unknown escape sequence"},
		{`'\U'`, CHAR, 3, `'\U'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\U0'`, CHAR, 4, `'\U0'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\U00'`, CHAR, 5, `'\U00'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\U000'`, CHAR, 6, `'\U000'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\U0000'`, CHAR, 7, `'\U0000'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\U00000'`, CHAR, 8, `'\U00000'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\U000000'`, CHAR, 9, `'\U000000'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\U0000000'`, CHAR, 10, `'\U0000000'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\U00000000'`, CHAR, 0, `'\U00000000'`, ""},
		{`'\U0000000`, CHAR, 10, `'\U0000000`, "escape sequence not terminated"},
		{`'\Uffffffff'`, CHAR, 2, `'\Uffffffff'`, "escape sequence is invalid Unicode code point"},
		{`'\`, CHAR, 2, `'\`, "escape sequence not terminated"},
		{`'\u'`, CHAR, 3, `'\u'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\u0'`, CHAR, 4, `'\u0'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\u00'`, CHAR, 5, `'\u00'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\u000'`, CHAR, 6, `'\u000'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\u0000'`, CHAR, 0, `'\u0000'`, ""},
		{`'\u000`, CHAR, 6, `'\u000`, "escape sequence not terminated"},
		{`'\x'`, CHAR, 3, `'\x'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\x0'`, CHAR, 4, `'\x0'`, "illegal character U+0027 ''' in escape sequence"},
		{`'\x00'`, CHAR, 0, "", ""},
		{`'\x0g'`, CHAR, 4, `'\x0g'`, "illegal character U+0067 'g' in escape sequence"},
		{`'`, CHAR, 0, `'`, "rune literal not terminated"},
		{`…`, ILLEGAL, 0, "", "illegal character U+2026 '…'"},
	} {
		s := newScanner(fmt.Sprintf("%d.go", itest), []byte(test.src))
		if !s.scan() && s.tok.ch != int32(EOF) {
			t.Fatalf("%v: unexpected EOF", itest)
		}

		if g, e := s.tok.token(), test.ch; g != e {
			t.Errorf("%v: %q: got %s, expected %s", itest, test.src, g, e)
		}
		switch {
		case len(s.errs) != 0:
			if g, e := s.errs[0].err.Error(), test.err; g != e {
				t.Errorf("%v: %q: got err %q, expected %q", itest, test.src, g, test.err)
			}
			if g, e := s.errs[0].pos.Offset, test.off; g != e {
				t.Errorf("%v: %q: got error offset %d, expected %d", itest, test.src, g, e)
			}
		default:
			if test.err != "" {
				t.Errorf("%q: missing error %q", test.src, test.err)
			}
		}
	}
}

func testNumbers(t *testing.T) {
	for itest, test := range []struct {
		tok              token.Token
		src, tokens, err string
	}{
		// binaries
		{token.INT, "0b0", "0b0", ""},
		{token.INT, "0b1010", "0b1010", ""},
		{token.INT, "0B1110", "0B1110", ""},

		{token.INT, "0b", "0b", "binary literal has no digits"},
		{token.INT, "0b0190", "0b0190", "invalid digit '9' in binary literal"},
		{token.INT, "0b01a0", "0b01 a0", ""}, // only accept 0-9

		{token.FLOAT, "0b.", "0b.", "invalid radix point in binary literal"},
		{token.FLOAT, "0b.1", "0b.1", "invalid radix point in binary literal"},
		{token.FLOAT, "0b1.0", "0b1.0", "invalid radix point in binary literal"},
		{token.FLOAT, "0b1e10", "0b1e10", "'e' exponent requires decimal mantissa"},
		{token.FLOAT, "0b1P-1", "0b1P-1", "'P' exponent requires hexadecimal mantissa"},

		{token.IMAG, "0b10i", "0b10i", ""},
		{token.IMAG, "0b10.0i", "0b10.0i", "invalid radix point in binary literal"},

		// octals
		{token.INT, "0o0", "0o0", ""},
		{token.INT, "0o1234", "0o1234", ""},
		{token.INT, "0O1234", "0O1234", ""},

		{token.INT, "0o", "0o", "octal literal has no digits"},
		{token.INT, "0o8123", "0o8123", "invalid digit '8' in octal literal"},
		{token.INT, "0o1293", "0o1293", "invalid digit '9' in octal literal"},
		{token.INT, "0o12a3", "0o12 a3", ""}, // only accept 0-9

		{token.FLOAT, "0o.", "0o.", "invalid radix point in octal literal"},
		{token.FLOAT, "0o.2", "0o.2", "invalid radix point in octal literal"},
		{token.FLOAT, "0o1.2", "0o1.2", "invalid radix point in octal literal"},
		{token.FLOAT, "0o1E+2", "0o1E+2", "'E' exponent requires decimal mantissa"},
		{token.FLOAT, "0o1p10", "0o1p10", "'p' exponent requires hexadecimal mantissa"},

		{token.IMAG, "0o10i", "0o10i", ""},
		{token.IMAG, "0o10e0i", "0o10e0i", "'e' exponent requires decimal mantissa"},

		// 0-octals
		{token.INT, "0", "0", ""},
		{token.INT, "0123", "0123", ""},

		{token.INT, "08123", "08123", "invalid digit '8' in octal literal"},
		{token.INT, "01293", "01293", "invalid digit '9' in octal literal"},
		{token.INT, "0F.", "0 F .", ""}, // only accept 0-9
		{token.INT, "0123F.", "0123 F .", ""},
		{token.INT, "0123456x", "0123456 x", ""},

		// decimals
		{token.INT, "1", "1", ""},
		{token.INT, "1234", "1234", ""},

		{token.INT, "1f", "1 f", ""}, // only accept 0-9

		{token.IMAG, "0i", "0i", ""},
		{token.IMAG, "0678i", "0678i", ""},

		// decimal floats
		{token.FLOAT, "0.", "0.", ""},
		{token.FLOAT, "123.", "123.", ""},
		{token.FLOAT, "0123.", "0123.", ""},

		{token.FLOAT, ".0", ".0", ""},
		{token.FLOAT, ".123", ".123", ""},
		{token.FLOAT, ".0123", ".0123", ""},

		{token.FLOAT, "0.0", "0.0", ""},
		{token.FLOAT, "123.123", "123.123", ""},
		{token.FLOAT, "0123.0123", "0123.0123", ""},

		{token.FLOAT, "0e0", "0e0", ""},
		{token.FLOAT, "123e+0", "123e+0", ""},
		{token.FLOAT, "0123E-1", "0123E-1", ""},

		{token.FLOAT, "0.e+1", "0.e+1", ""},
		{token.FLOAT, "123.E-10", "123.E-10", ""},
		{token.FLOAT, "0123.e123", "0123.e123", ""},

		{token.FLOAT, ".0e-1", ".0e-1", ""},
		{token.FLOAT, ".123E+10", ".123E+10", ""},
		{token.FLOAT, ".0123E123", ".0123E123", ""},

		{token.FLOAT, "0.0e1", "0.0e1", ""},
		{token.FLOAT, "123.123E-10", "123.123E-10", ""},
		{token.FLOAT, "0123.0123e+456", "0123.0123e+456", ""},

		{token.FLOAT, "0e", "0e", "exponent has no digits"},
		{token.FLOAT, "0E+", "0E+", "exponent has no digits"},
		{token.FLOAT, "1e+f", "1e+ f", "exponent has no digits"},
		{token.FLOAT, "0p0", "0p0", "'p' exponent requires hexadecimal mantissa"},
		{token.FLOAT, "1.0P-1", "1.0P-1", "'P' exponent requires hexadecimal mantissa"},

		{token.IMAG, "0.i", "0.i", ""},
		{token.IMAG, ".123i", ".123i", ""},
		{token.IMAG, "123.123i", "123.123i", ""},
		{token.IMAG, "123e+0i", "123e+0i", ""},
		{token.IMAG, "123.E-10i", "123.E-10i", ""},
		{token.IMAG, ".123E+10i", ".123E+10i", ""},

		// hexadecimals
		{token.INT, "0x0", "0x0", ""},
		{token.INT, "0x1234", "0x1234", ""},
		{token.INT, "0xcafef00d", "0xcafef00d", ""},
		{token.INT, "0XCAFEF00D", "0XCAFEF00D", ""},

		{token.INT, "0x", "0x", "hexadecimal literal has no digits"},
		{token.INT, "0x1g", "0x1 g", ""},

		{token.IMAG, "0xf00i", "0xf00i", ""},

		// hexadecimal floats
		{token.FLOAT, "0x0p0", "0x0p0", ""},
		{token.FLOAT, "0x12efp-123", "0x12efp-123", ""},
		{token.FLOAT, "0xABCD.p+0", "0xABCD.p+0", ""},
		{token.FLOAT, "0x.0189P-0", "0x.0189P-0", ""},
		{token.FLOAT, "0x1.ffffp+1023", "0x1.ffffp+1023", ""},

		{token.FLOAT, "0x.", "0x.", "hexadecimal literal has no digits"},
		{token.FLOAT, "0x0.", "0x0.", "hexadecimal mantissa requires a 'p' exponent"},
		{token.FLOAT, "0x.0", "0x.0", "hexadecimal mantissa requires a 'p' exponent"},
		{token.FLOAT, "0x1.1", "0x1.1", "hexadecimal mantissa requires a 'p' exponent"},
		{token.FLOAT, "0x1.1e0", "0x1.1e0", "hexadecimal mantissa requires a 'p' exponent"},
		{token.FLOAT, "0x1.2gp1a", "0x1.2 gp1a", "hexadecimal mantissa requires a 'p' exponent"},
		{token.FLOAT, "0x0p", "0x0p", "exponent has no digits"},
		{token.FLOAT, "0xeP-", "0xeP-", "exponent has no digits"},
		{token.FLOAT, "0x1234PAB", "0x1234P AB", "exponent has no digits"},
		{token.FLOAT, "0x1.2p1a", "0x1.2p1 a", ""},

		{token.IMAG, "0xf00.bap+12i", "0xf00.bap+12i", ""},

		// separators
		{token.INT, "0b_1000_0001", "0b_1000_0001", ""},
		{token.INT, "0o_600", "0o_600", ""},
		{token.INT, "0_466", "0_466", ""},
		{token.INT, "1_000", "1_000", ""},
		{token.FLOAT, "1_000.000_1", "1_000.000_1", ""},
		{token.IMAG, "10e+1_2_3i", "10e+1_2_3i", ""},
		{token.INT, "0x_f00d", "0x_f00d", ""},
		{token.FLOAT, "0x_f00d.0p1_2", "0x_f00d.0p1_2", ""},

		{token.INT, "0b__1000", "0b__1000", "'_' must separate successive digits"},
		{token.INT, "0o60___0", "0o60___0", "'_' must separate successive digits"},
		{token.INT, "0466_", "0466_", "'_' must separate successive digits"},
		{token.FLOAT, "1_.", "1_.", "'_' must separate successive digits"},
		{token.FLOAT, "0._1", "0._1", "'_' must separate successive digits"},
		{token.FLOAT, "2.7_e0", "2.7_e0", "'_' must separate successive digits"},
		{token.IMAG, "10e+12_i", "10e+12_i", "'_' must separate successive digits"},
		{token.INT, "0x___0", "0x___0", "'_' must separate successive digits"},
		{token.FLOAT, "0x1.0_p0", "0x1.0_p0", "'_' must separate successive digits"},
	} {
		s := newScanner(fmt.Sprintf("%d.go", itest), []byte(test.src))
		for i, want := range strings.Split(test.tokens, " ") {
			if !s.scan() {
				t.Errorf("%v: unecpected EOF", itest)
				continue
			}

			if i == 0 {
				if g, e := s.tok.token(), test.tok; g != e {
					t.Errorf("%q: got token %s; want %s", test.src, g, e)
				}

				switch {
				case len(s.errs) != 0:
					if g, e := s.errs[0].err.Error(), test.err; g != e {
						t.Errorf("%v: %q: got err %q, expected %q", i, test.src, g, test.err)
					}
				default:
					if test.err != "" {
						t.Errorf("%q: missing error %q", test.src, test.err)
					}
				}
			}

			if g, e := s.token().Src(), want; g != e {
				t.Errorf("%q: got literal %q (%s); want %s", test.src, g, s.token().Ch(), e)
			}
		}

		// make sure we read all
		s.scan()
		if s.tok.ch == int32(SEMICOLON) {
			s.scan()
		}
		if s.tok.ch != int32(EOF) {
			t.Errorf("%q: got %s; want EOF", test.src, s.tok.token())
		}
	}
}
