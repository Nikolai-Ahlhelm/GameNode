// Modified code from
//
//	https://cs.opensource.google/go/x/exp/+/78e5e7837ae670883fc07e0620a1a8ac87bbc72d:ebnflint/ebnflint.go

// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/exp/ebnf"
)

// Markers around EBNF sections in .html files
var (
	open  = []byte(`<pre class="ebnf">`)
	close = []byte(`</pre>`)
)

func extractSpecEBNF(src []byte) []byte {
	var buf bytes.Buffer

	for {
		// i = beginning of EBNF text
		i := bytes.Index(src, open)
		if i < 0 {
			break // no EBNF found - we are done
		}
		i += len(open)

		// write as many newlines as found in the excluded text
		// to maintain correct line numbers in error messages
		for _, ch := range src[0:i] {
			if ch == '\n' {
				buf.WriteByte('\n')
			}
		}

		// j = end of EBNF text (or end of source)
		j := bytes.Index(src[i:], close) // close marker
		if j < 0 {
			j = len(src) - i
		}
		j += i

		// copy EBNF text
		buf.Write(src[i:j])

		// advance
		src = src[j:]
	}

	return buf.Bytes()
}

func verifySpecEBNF(name, start string, r io.Reader) ([]byte, ebnf.Grammar, error) {
	if r == nil {
		f, err := os.Open(name)
		if err != nil {
			return nil, nil, err
		}
		defer f.Close()
		r = f
	}

	src, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}

	if filepath.Ext(name) == ".html" || bytes.Contains(src, open) {
		src = extractSpecEBNF(src)
	}

	grammar, err := ebnf.Parse(name, bytes.NewBuffer(src))
	if err != nil {
		return nil, nil, err
	}

	return src, grammar, ebnf.Verify(grammar, start)
}
