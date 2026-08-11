// Copyright 2022 The Gc Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gc // modernc.org/gc/v3

import (
	"encoding/hex"
	"flag"
	"fmt"
	goparser "go/parser"
	goscanner "go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pmezard/go-difflib/difflib"
	"golang.org/x/tools/go/packages"
	"modernc.org/mathutil"
)

func stack() string { return string(debug.Stack()) }

const (
	defaultSrc = "."
)

var (
	oBSrc              = flag.String("bsrc", runtime.GOROOT(), "")
	oHeap              = flag.Bool("heap", false, "")
	oNReport           = flag.Bool("nreport", false, "")
	oRE                = flag.String("re", "", "")
	oReport            = flag.Bool("report", false, "")
	oSrc               = flag.String("src", defaultSrc, "")
	oTrc               = flag.Bool("trc", false, "")
	oTrcObjects        = flag.Bool("trco", false, "")
	oTrcExpectedErrors = flag.Bool("trcee", false, "")

	re     *regexp.Regexp
	wd     string
	probes probeA
)

type probeA [4]int32

func (p *probeA) inc(i int)     { atomic.AddInt32(&p[i], 1) }
func (p *probeA) incN(i, n int) { atomic.AddInt32(&p[i], int32(n)) }

func TestMain(m *testing.M) {
	flag.BoolVar(&noBack, "noback", false, "panic on parser back")
	flag.BoolVar(&panicBack, "panicback", false, "panic on parser back")
	flag.BoolVar(&trcTODOs, "trctodo", false, "")
	flag.BoolVar(&extendedErrors, "exterr", false, "")
	flag.BoolVar(&trcErrors, "trce", false, "")
	flag.Parse()
	if s := *oRE; s != "" {
		re = regexp.MustCompile(s)
	}

	var err error
	if wd, err = os.Getwd(); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}

type golden struct {
	a  []string
	f  *os.File
	mu sync.Mutex
	t  *testing.T

	discard bool
}

func newGolden(t *testing.T, fn string) *golden {
	if re != nil || *oReport || *oSrc != defaultSrc {
		return &golden{discard: true}
	}

	f, err := os.Create(filepath.FromSlash(fn))
	if err != nil { // Possibly R/O fs in a VM
		base := filepath.Base(filepath.FromSlash(fn))
		f, err = os.CreateTemp("", base)
		if err != nil {
			t.Fatal(err)
		}

		t.Logf("writing results to %s\n", f.Name())
	}

	return &golden{t: t, f: f}
}

func (g *golden) w(s string, args ...interface{}) {
	if g.discard {
		return
	}

	g.mu.Lock()

	defer g.mu.Unlock()

	if s = strings.TrimRight(s, " \t\n\r"); !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	g.a = append(g.a, fmt.Sprintf(s, args...))
}

func (g *golden) close() {
	if g.discard || g.f == nil {
		return
	}

	defer func() { g.f = nil }()

	sort.Strings(g.a)
	if _, err := g.f.WriteString(strings.Join(g.a, "")); err != nil {
		g.t.Fatal(err)
	}

	if err := g.f.Sync(); err != nil {
		g.t.Fatal(err)
	}

	if err := g.f.Close(); err != nil {
		g.t.Fatal(err)
	}
}

type testParallel struct {
	a                  *analyzer
	objects            []interface{}
	errors             []error
	limit              chan struct{}
	maxBacktrackOrigin string
	maxBacktrackPath   string
	maxBacktrackPos    string
	maxBacktracksPath  string
	maxBudgetPath      string
	maxDuration        time.Duration
	maxDurationPath    string
	minToksPath        string
	sync.Mutex
	wg sync.WaitGroup

	maxBacktrack      int
	maxBacktrackToks  int
	maxBacktracks     int
	maxBacktracksToks int
	maxDurationToks   int
	maxBudget         int
	maxBudgetToks     int
	minToks           int

	allToks  int32
	packages int32
	fails    int32
	files    int32
	ok       int32
	skipped  int32
}

func newTestParallel(limit int) *testParallel {
	if limit <= 0 {
		limit = runtime.GOMAXPROCS(0)
	}
	return &testParallel{
		a:     newAnalyzer(),
		limit: make(chan struct{}, limit),
	}
}

func (p *testParallel) addPackage()    { atomic.AddInt32(&p.packages, 1) }
func (p *testParallel) addFail()       { atomic.AddInt32(&p.fails, 1) }
func (p *testParallel) addFile()       { atomic.AddInt32(&p.files, 1) }
func (p *testParallel) addFileN(n int) { atomic.AddInt32(&p.files, int32(n)) }
func (p *testParallel) addOk()         { atomic.AddInt32(&p.ok, 1) }
func (p *testParallel) addOkN(n int)   { atomic.AddInt32(&p.ok, int32(n)) }
func (p *testParallel) addSkipped()    { atomic.AddInt32(&p.skipped, 1) }
func (p *testParallel) addToks(n int)  { atomic.AddInt32(&p.allToks, int32(n)) }

func (p *testParallel) addObject(obj interface{}) {
	p.Lock()
	defer p.Unlock()

	p.objects = append(p.objects, obj)
}

func (p *testParallel) recordMaxDuration(path string, d time.Duration, toks int) {
	p.Lock()
	defer p.Unlock()

	if d > p.maxDuration {
		p.maxDuration = d
		p.maxDurationPath = path
		p.maxDurationToks = toks
	}
}

func (p *testParallel) recordMaxBacktrack(path string, back, toks int, pos, origin string) {
	p.Lock()
	defer p.Unlock()

	if back > p.maxBacktrack {
		p.maxBacktrack = back
		p.maxBacktrackOrigin = origin
		p.maxBacktrackPos = pos
		p.maxBacktrackPath = path
		p.maxBacktrackToks = toks
	}
}

func (p *testParallel) recordMaxBacktracks(path string, back, toks int) {
	p.Lock()
	defer p.Unlock()

	if back > p.maxBacktracks {
		p.maxBacktracks = back
		p.maxBacktracksPath = path
		p.maxBacktracksToks = toks
	}
}

func (p *testParallel) recordMaxBudget(path string, budget, toks int) {
	p.Lock()
	defer p.Unlock()

	if budget > p.maxBudget {
		p.maxBudget = budget
		p.maxBudgetToks = toks
		p.maxBudgetPath = path
	}
}

func (p *testParallel) recordMinToks(path string, toks int) {
	p.Lock()
	defer p.Unlock()

	if p.minToks == 0 || toks < p.minToks {
		p.minToks = toks
		p.minToksPath = path
	}
}

func (p *testParallel) err(err error) {
	if err == nil {
		return
	}

	s := err.Error()
	if x := strings.Index(s, "TODO"); x >= 0 {
		fmt.Println(s[x:])
	}
	p.Lock()
	p.errors = append(p.errors, err)
	p.Unlock()
}

func (p *testParallel) exec(run func() error) {
	p.limit <- struct{}{}
	p.wg.Add(1)

	go func() {
		defer func() {
			p.wg.Done()
			<-p.limit
		}()

		p.err(run())
	}()
}

func (p *testParallel) wait() error {
	p.wg.Wait()
	if len(p.errors) == 0 {
		return nil
	}

	var a []string
	for _, v := range p.errors {
		a = append(a, v.Error())
	}
	return fmt.Errorf("%s", strings.Join(a, "\n"))
}

func TestScanner(t *testing.T) {
	p := newTestParallel(0)
	t.Run("errors", func(t *testing.T) { testScanErrors(t) })
	t.Run("numbers", func(t *testing.T) { testNumbers(t) })
	t.Run("src", func(t *testing.T) { testScan(p, t, *oSrc) })
	t.Run("GOROOT", func(t *testing.T) { testScan(p, t, runtime.GOROOT()) })
	if err := p.wait(); err != nil {
		t.Error(err)
	}
	t.Logf("TOTAL files %v, ok %v, fail %v", h(p.files), h(p.ok), h(p.fails))
}

func testScan(p *testParallel, t *testing.T, root string) {
	if err := filepath.Walk(root, func(path0 string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		if re != nil && !re.MatchString(path0) {
			return nil
		}

		if filepath.Ext(path0) != ".go" {
			return nil
		}

		path := path0
		p.addFile()
		p.exec(func() error {
			if *oTrc {
				fmt.Fprintln(os.Stderr, path)
			}

			b, err := os.ReadFile(path)
			if err != nil {
				p.addFail()
				return err
			}

			fs := token.NewFileSet()
			fi := fs.AddFile(path, -1, len(b))
			var s0 goscanner.Scanner
			var err0 error
			s0.Init(fi, b, func(pos token.Position, msg string) {
				err0 = fmt.Errorf("%v: %s", pos, msg)
			}, 0)
			s := newScanner(path, b)
			for {
				pos0, tok0, lit0 := s0.Scan()
				position0 := fi.Position(pos0)
				eof0 := tok0 == token.EOF
				// trc("", position0, tok0, lit0, eof0)
				eof := !s.scan()
				// trc("", s.token().Position(), s.token().Ch(), s.token().Src(), s.eof)
				err := s.errs.Err()
				if g, e := s.token().Ch(), tok0; g != e {
					p.addFail()
					return fmt.Errorf("%v: token, got %v, expected %v", position0, g, e)
				}

				if g, e := err, err0; (g != nil) != (e != nil) {
					p.addFail()
					return fmt.Errorf("%v: error, got %v, expected %v", position0, g, e)
				}

				if err != nil {
					p.addOk()
					return nil
				}

				g, e := s.token().Src(), lit0
				if tok0 == token.STRING && strings.HasPrefix(e, "`") {
					// Specs: Carriage return characters ('\r') inside raw string literals are
					// discarded from the raw string value.
					g = strings.ReplaceAll(g, "\r", "")
				}
				if g != e {
					switch {
					case tok0 == token.SEMICOLON && lit0 != ";":
						// Ok, our result for injected semis is different.
					case noGoLit(s.token().Ch()):
						// Ok, go/scanner does not return the literal string.
					default:
						p.addFail()
						return fmt.Errorf("%v: source, got %q(`%[2]s`), expected %q(`%[3]s`)", position0, g, e)
					}
				}

				if g, e := s.token().Position().String(), position0.String(); g != e {
					ok := false
					switch {
					case eof || eof0:
						if a, b := s.token().Position().Offset, position0.Offset; a == b {
							ok = true
						}
					case tok0 == token.SEMICOLON && lit0 == "\n":
						ok = s.token().Position().Filename == position0.Filename && s.token().Position().Line == position0.Line
					}
					if !ok {
						p.addFail()
						return fmt.Errorf("%v: position, got %v (%v: %s %q)", e, g, path, tok0, lit0)
					}
				}

				if g, e := eof, eof0; g != e {
					p.addFail()
					return fmt.Errorf("%v: EOF, got %v, expected %v", position0, g, e)
				}

				if eof {
					break
				}
			}
			p.addOk()
			return nil
		})
		return nil
	}); err != nil {
		t.Error(err)
	}
}

func noGoLit(c token.Token) bool {
	switch c {
	case
		ADD,
		ADD_ASSIGN,
		AND,
		AND_ASSIGN,
		AND_NOT,
		AND_NOT_ASSIGN,
		ARROW,
		ASSIGN,
		COLON,
		COMMA,
		DEC,
		DEFINE,
		ELLIPSIS,
		EQL,
		GEQ,
		GTR,
		INC,
		LAND,
		LBRACE,
		LBRACK,
		LEQ,
		LOR,
		LPAREN,
		LSS,
		MUL,
		MUL_ASSIGN,
		NEQ,
		NOT,
		OR,
		OR_ASSIGN,
		PERIOD,
		QUO,
		QUO_ASSIGN,
		RBRACE,
		RBRACK,
		REM,
		REM_ASSIGN,
		RPAREN,
		SHL,
		SHL_ASSIGN,
		SHR,
		SHR_ASSIGN,
		SUB,
		SUB_ASSIGN,
		TILDE,
		XOR,
		XOR_ASSIGN:

		return true
	}

	return false
}

var falseNegatives = []string{
	"golang.org/x/tools/go/analysis/passes/unreachable/testdata/src/a/a.go",
}

func isKnownBadFile(fn string, pos token.Position, err error) bool {
	return isKnownBadFile0(fn, pos) || isKnownBadFile1(fn, err)
}

func isKnownBadFile1(fn string, err error) bool {
	if x, ok := err.(errList); ok {
		for _, v := range x {
			if isKnownBadFile0(fn, v.pos) {
				return true
			}
		}
	}
	return false
}

func isKnownBadFile0(fn string, pos token.Position) bool {
	fs := token.NewFileSet()
	ast, err := goparser.ParseFile(fs, fn, nil, goparser.ParseComments|goparser.DeclarationErrors)
	if err != nil {
		return true
	}

	for _, v := range ast.Comments {
		for _, w := range v.List {
			if strings.Contains(w.Text, "ERROR") && fs.PositionFor(w.Slash, true).Line == pos.Line {
				return true
			}
		}
	}

	s := filepath.ToSlash(fn)
	for _, k := range falseNegatives {
		if strings.Contains(s, k) {
			return true
		}
	}

	return false
}

func TestParser(t *testing.T) {
	gld := newGolden(t, fmt.Sprintf("testdata/test_parser_%s_%s.golden", runtime.GOOS, runtime.GOARCH))

	defer gld.close()

	var ms0, ms runtime.MemStats
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms0)
	p := newTestParallel(0)
	t.Run("src", func(t *testing.T) { testParser(p, t, *oSrc, gld) })
	t.Run("goroot", func(t *testing.T) { testParser(p, t, runtime.GOROOT(), gld) })
	if err := p.wait(); err != nil {
		t.Error(err)
	}
	t.Logf("TOTAL files %v, toks %v, skip %v, ok %v, fail %v", h(p.files), h(p.allToks), h(p.skipped), h(p.ok), h(p.fails))
	if p.fails != 0 {
		t.Logf("Shortest failing file: %s, %v tokens", p.minToksPath, p.minToks)
		return
	}

	t.Logf("Max backtrack: %s, %v for %v tokens\n\t%v (%v:)", p.maxBacktrackPath, h(p.maxBacktrack), h(p.maxBacktrackToks), p.maxBacktrackPos, p.maxBacktrackOrigin)
	t.Logf("Max backtracks: %s, %v for %v tokens", p.maxBacktracksPath, h(p.maxBacktracks), h(p.maxBacktracksToks))
	t.Logf("Max budget used: %s, %v for %v tokens", p.maxBudgetPath, h(p.maxBudget), h(p.maxBudgetToks))
	t.Logf("Max duration: %s, %v for %v tokens", p.maxDurationPath, p.maxDuration, h(p.maxDurationToks))
	if *oReport {
		t.Logf("\n%s", p.a.report())
	}
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms)
	if *oHeap && *oSrc == defaultSrc {
		t.Logf("ast count %v, heap %s", h(len(p.objects)), h(ms.HeapAlloc-ms0.HeapAlloc))
		if *oNReport {
			t.Logf("AST breakdown\n%s", nodeReport(p.objects...))
		}
	}
	t.Log(h(probes[0]), h(probes[1]), h(probes[2]), h(probes[3]))
}

func testParser(p *testParallel, t *testing.T, root string, gld *golden) {
	if err := filepath.Walk(filepath.FromSlash(root), func(path0 string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		if re != nil && !re.MatchString(path0) {
			return nil
		}

		if filepath.Ext(path0) != ".go" {
			return nil
		}

		p.addFile()
		path := path0
		p.exec(func() (err error) {
			if *oTrc {
				fmt.Fprintln(os.Stderr, path)
			}

			var pp *parser
			t0 := time.Now()

			defer func() {
				if err != nil {
					p.addFail()
					if pp != nil {
						p.recordMinToks(path, len(pp.s.toks))
					}
				}
				if pp != nil {
					p.recordMaxDuration(path, time.Since(t0), len(pp.s.toks))
					p.addToks(len(pp.s.toks))
					from := pp.s.toks[pp.maxBackRange[0]].position(pp.s.source)
					hi := mathutil.Min(pp.maxBackRange[1], len(pp.s.toks)-1)
					to := pp.s.toks[hi].position(pp.s.source)
					p.recordMaxBacktrack(path, pp.maxBack, len(pp.s.toks), fmt.Sprintf("%v: - %v:", from, to), pp.maxBackOrigin)
					p.recordMaxBacktracks(path, pp.backs, len(pp.s.toks))
					p.recordMaxBudget(path, parserBudget-pp.budget, len(pp.s.toks))
					if *oReport {
						p.a.merge(pp.a)
					}
				}
			}()

			b, err := os.ReadFile(path)
			if err != nil {
				return errorf("%s: %v", path, err)
			}

			pp = newParser(newScope(nil, PackageScope), path, b, *oReport)
			pp.reportDeclarationErrors = true
			ast, err := pp.parse()
			if err != nil {
				if isKnownBadFile(path, pp.errPosition(), err) {
					if *oTrcExpectedErrors {
						t.Log(err)
					}
					pp = nil
					p.addSkipped()
					return nil
				}

				return errorf("%s", err)
			}

			// trc("\n%s", dump(ast))
			srcA := string(b)
			srcB := ast.Source(true)
			if srcA != srcB {
				diff := difflib.UnifiedDiff{
					A:        difflib.SplitLines(srcA),
					B:        difflib.SplitLines(srcB),
					FromFile: "expected",
					ToFile:   "got",
					Context:  0,
				}
				s, _ := difflib.GetUnifiedDiffString(diff)
				return errorf(
					"%v: ast.Source differs\n%v\n--- expexted\n%s\n\n--- got\n%s\n\n--- expected\n%s\n--- got\n%s",
					path0, s, srcA, srcB, hex.Dump([]byte(srcA)), hex.Dump([]byte(srcB)),
				)
			}

			if *oHeap && *oSrc == defaultSrc {
				p.addObject(ast)
			}
			p.addOk()
			gld.w("%s\n", path)
			return nil
		})
		return nil
	}); err != nil {
		t.Error(err)
	}
}

func TestGoParser(t *testing.T) {
	gld := newGolden(t, fmt.Sprintf("testdata/test_goparser_%s_%s.golden", runtime.GOOS, runtime.GOARCH))

	defer gld.close()

	var ms0, ms runtime.MemStats
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms0)
	p := newTestParallel(0)
	t.Run("src", func(t *testing.T) { testGoParser(p, t, *oSrc, gld) })
	t.Run("goroot", func(t *testing.T) { testGoParser(p, t, runtime.GOROOT(), gld) })
	if err := p.wait(); err != nil {
		t.Error(err)
	}
	t.Logf("TOTAL files %v, skip %v, ok %v, fail %v", h(p.files), h(p.skipped), h(p.ok), h(p.fails))
	t.Logf("Max duration: %s, %v for %v tokens", p.maxDurationPath, p.maxDuration, h(p.maxDurationToks))
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms)
	if *oHeap && *oSrc == defaultSrc {
		t.Logf("ast count %v, heap %s", h(len(p.objects)), h(ms.HeapAlloc-ms0.HeapAlloc))
	}
}

func testGoParser(p *testParallel, t *testing.T, root string, gld *golden) {
	if err := filepath.Walk(filepath.FromSlash(root), func(path0 string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		if re != nil && !re.MatchString(path0) {
			return nil
		}

		if filepath.Ext(path0) != ".go" {
			return nil
		}

		p.addFile()
		path := path0
		p.exec(func() (err error) {
			if *oTrc {
				fmt.Fprintln(os.Stderr, path)
			}

			t0 := time.Now()

			defer func() {
				if err != nil {
					p.addFail()
					return
				}

				p.recordMaxDuration(path, time.Since(t0), -1) // Number of tokens unknown.
			}()

			b, err := os.ReadFile(path)
			if err != nil {
				return errorf("%s: %v", path, err)
			}

			ast, err := goparser.ParseFile(token.NewFileSet(), path, b, goparser.DeclarationErrors)
			if err != nil {
				if pos, ok := extractPos(err.Error()); !ok || isKnownBadFile0(path, pos) {
					p.addSkipped()
					return nil
				}

				return errorf("%s", err)
			}

			if *oHeap && *oSrc == defaultSrc {
				p.addObject(ast)
			}
			p.addOk()
			gld.w("%s\n", path)
			return nil
		})
		return nil
	}); err != nil {
		t.Error(err)
	}
}

func BenchmarkParser(b *testing.B) {
	var sum int64
	root := *oBSrc
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := filepath.Walk(filepath.FromSlash(root), func(path0 string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() {
				return nil
			}

			if re != nil && !re.MatchString(path0) {
				return nil
			}

			if filepath.Ext(path0) != ".go" {
				return nil
			}

			path := path0
			if err := func() (err error) {
				var pp *parser
				b, err := os.ReadFile(path)
				sum += int64(len(b))
				if err != nil {
					return errorf("%s: %v", path, err)
				}

				pp = newParser(newScope(nil, PackageScope), path, b, *oReport)
				pp.parse()
				return nil
			}(); err != nil {
				b.Fatal(err)
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(sum)
}

func BenchmarkGoParser(b *testing.B) {
	var sum int64
	root := *oBSrc
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := filepath.Walk(filepath.FromSlash(root), func(path0 string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}

			if re != nil && !re.MatchString(path0) {
				return nil
			}

			if filepath.Ext(path0) != ".go" {
				return nil
			}

			path := path0
			if err := func() (err error) {
				b, err := os.ReadFile(path)
				sum += int64(len(b))
				if err != nil {
					return errorf("%s: %v", path, err)
				}

				if _, err = goparser.ParseFile(token.NewFileSet(), path, b, goparser.DeclarationErrors); err != nil {
					if pos, ok := extractPos(err.Error()); !ok || isKnownBadFile0(path, pos) {
						return nil
					}

					return errorf("%s", err)
				}

				return nil
			}(); err != nil {
				b.Fatal(err)
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.SetBytes(sum)
}

func TestNewConfig(t *testing.T) {
	_, err := NewConfig()
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewPackage(t *testing.T) {
	debug.FreeOSMemory()
	cfg, err := NewConfig(ConfigCache(MustNewCache(1e3)))
	if err != nil {
		t.Fatal(err)
	}

	p := newTestParallel(0)
	root := filepath.Join(runtime.GOROOT(), "src")
	var ms0, ms runtime.MemStats
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms0)
	t.Run("GOROOT", func(t *testing.T) { testNewPackage(cfg, p, t, root, TypeCheckNone) })
	if root = *oSrc; root != defaultSrc {
		t.Run("src", func(t *testing.T) { testNewPackage(cfg, p, t, root, TypeCheckNone) })
	}
	if err := p.wait(); err != nil {
		t.Error(err)
	}
	t.Logf("TOTAL packages %v, files %v, skip %v, ok %v, fail %v", h(p.packages), h(p.files), h(p.skipped), h(p.ok), h(p.fails))
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms)
	if *oHeap && *oSrc == defaultSrc {
		t.Logf("pkg count %v, heap %s", h(len(p.objects)), h(ms.HeapAlloc-ms0.HeapAlloc))
	}
}

func testNewPackage(cfg *Config, p *testParallel, t *testing.T, root string, typeCheck TypeCheck) {
	if err := filepath.Walk(root, func(path0 string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() || strings.Contains(path0, "/testdata/") {
			return nil
		}

		if re != nil && !re.MatchString(path0) {
			return nil
		}

		matches, err := filepath.Glob(filepath.Join(path0, "*.go"))
		if err != nil {
			return err
		}

		if len(matches) == 0 {
			return nil
		}

		p.addPackage()
		importPath := filepath.ToSlash(path0[len(root)+1:])
		p.exec(func() error {
			if *oTrc {
				fmt.Fprintln(os.Stderr, importPath)
			}
			pkg, err := cfg.NewPackage("", importPath, "", nil, false, typeCheck)
			if err != nil {
				p.addFail()
				return err

			}

			for path, err := range pkg.InvalidGoFiles {
				switch x := err.(type) {
				case errList:
					pos := x[0].pos
					if !isKnownBadFile0("/"+pos.Filename, pos) {
						p.addFail()
						return err
					}
				default:
					panic(todo("%T %q %q", x, x, path))
				}
				p.addSkipped()
				return nil
			}

			if *oHeap && *oSrc == defaultSrc {
				p.addObject(pkg)
			}
			p.addFileN(len(pkg.GoFiles))
			p.addOk()
			return nil
		})
		return nil
	}); err != nil {
		t.Error(err)
	}
}

func nodelyzer(n ...interface{}) (r map[reflect.Type]int) {
	r = map[reflect.Type]int{}
	for _, v := range n {
		nodelyzer0(r, v)
	}
	return r
}

var tokenType = reflect.TypeOf(Token{})

func nodelyzer0(m map[reflect.Type]int, n interface{}) {
	switch n.(type) {
	case nil, Token:
		// nop
	default:
		t := reflect.TypeOf(n)
		v := reflect.ValueOf(n)
		switch t.Kind() {
		case reflect.Pointer:
			if !v.IsZero() {
				nodelyzer0(m, v.Elem().Interface())
			}
		case reflect.Struct:
			m[t]++
			for i := 0; i < t.NumField(); i++ {
				if token.IsExported(t.Field(i).Name) {
					nodelyzer0(m, v.Field(i).Interface())
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				nodelyzer0(m, v.Index(i).Interface())
			}
		default:
			panic(todo("", t.Kind()))
		}
	}
}

func nodeReport(n ...interface{}) string {
	type line struct {
		t   string
		n   int
		sz  int
		sz1 int
	}
	var lines []line
	for k, v := range nodelyzer(n...) {
		lines = append(lines, line{k.String(), v, int(k.Size()) * v, int(k.Size())})
	}
	sort.Slice(lines, func(i, j int) bool {
		a, b := lines[i], lines[j]
		if a.sz < b.sz {
			return true
		}

		if a.sz > b.sz {
			return false
		}

		return a.t < b.t
	})
	var b strings.Builder
	var tn, tsz, csz int
	for _, v := range lines {
		csz += v.sz
		fmt.Fprintf(&b, "%40s x %10s = %13s á %3s %13s\n", v.t, h(v.n), h(v.sz), h(v.sz1), h(csz))
		tn += v.n
		tsz += v.sz
	}
	fmt.Fprintf(&b, "%40s x %10s = %13s á %3.0f\n", "<total>", h(tn), h(tsz), float64(tsz)/float64(tn))
	return b.String()
}

func TestGoNewPackage(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedFiles | packages.NeedSyntax}
	p := newTestParallel(1)
	root := filepath.Join(runtime.GOROOT(), "src")
	var ms0, ms runtime.MemStats
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms0)
	t.Run("GOROOT", func(t *testing.T) { testGoNewPackage(cfg, p, t, root) })
	if root = *oSrc; root != defaultSrc {
		t.Run("src", func(t *testing.T) { testGoNewPackage(cfg, p, t, root) })
	}
	if err := p.wait(); err != nil {
		t.Error(err)
	}
	t.Logf("TOTAL packages %v, files %v, skip %v, ok %v, fail %v", h(p.packages), h(p.files), h(p.skipped), h(p.ok), h(p.fails))
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms)
	if *oHeap && *oSrc == defaultSrc {
		t.Logf("pkg count %v, heap %s", h(len(p.objects)), h(ms.HeapAlloc-ms0.HeapAlloc))
	}
}

func testGoNewPackage(cfg *packages.Config, p *testParallel, t *testing.T, root string) {
	var importPaths []string
	if err := filepath.Walk(root, func(path0 string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() || strings.Contains(path0, "/testdata/") {
			return nil
		}

		if re != nil && !re.MatchString(path0) {
			return nil
		}

		matches, err := filepath.Glob(filepath.Join(path0, "*.go"))
		if err != nil {
			return err
		}

		if len(matches) == 0 {
			return nil
		}

		p.addPackage()
		importPath := filepath.ToSlash(path0[len(root)+1:])
		importPaths = append(importPaths, importPath)
		return nil
	}); err != nil {
		t.Error(err)
	}
	pkgs, err := packages.Load(cfg, importPaths...)
	if err != nil {
		t.Error(err)
		return
	}

	p.addOkN(len(pkgs))
	for _, v := range pkgs {
		p.addFileN(len(v.GoFiles))
		p.addObject(v)
	}
}

func TestTypeCheck(t *testing.T) {
	debug.FreeOSMemory()
	cfg, err := NewConfig(ConfigCache(MustNewCache(1e3)))
	if err != nil {
		t.Fatal(err)
	}

	p := newTestParallel(1)
	root := filepath.Join(runtime.GOROOT(), "src")
	var ms0, ms runtime.MemStats
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms0)
	t.Run("GOROOT", func(t *testing.T) { testNewPackage(cfg, p, t, root, TypeCheckAll) })
	if root = *oSrc; root != defaultSrc {
		t.Run("src", func(t *testing.T) { testNewPackage(cfg, p, t, root, TypeCheckAll) })
	}
	if err := p.wait(); err != nil {
		t.Error(err)
	}
	t.Logf("TOTAL packages %v, files %v, skip %v, ok %v, fail %v", h(p.packages), h(p.files), h(p.skipped), h(p.ok), h(p.fails))
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms)
	if *oHeap && *oSrc == defaultSrc {
		t.Logf("pkg count %v, heap %s", h(len(p.objects)), h(ms.HeapAlloc-ms0.HeapAlloc))
	}
	if *oTrcObjects {
		for _, v := range p.objects {
			pkg := v.(*Package)
			for _, v := range pkg.GoFiles {
				fmt.Println(filepath.Join(pkg.FSPath, v.Name()))
			}
		}
	}
}

func TestGoTypeCheck(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedTypesSizes}
	p := newTestParallel(1)
	root := filepath.Join(runtime.GOROOT(), "src")
	var ms0, ms runtime.MemStats
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms0)
	t.Run("GOROOT", func(t *testing.T) { testGoNewPackage(cfg, p, t, root) })
	if root = *oSrc; root != defaultSrc {
		t.Run("src", func(t *testing.T) { testGoNewPackage(cfg, p, t, root) })
	}
	if err := p.wait(); err != nil {
		t.Error(err)
	}
	t.Logf("TOTAL packages %v, files %v, skip %v, ok %v, fail %v", h(p.packages), h(p.files), h(p.skipped), h(p.ok), h(p.fails))
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms)
	if *oHeap && *oSrc == defaultSrc {
		t.Logf("pkg count %v, heap %s", h(len(p.objects)), h(ms.HeapAlloc-ms0.HeapAlloc))
	}
	if *oTrcObjects {
		for _, v := range p.objects {
			pkg := v.(*packages.Package)
			for _, v := range pkg.GoFiles {
				fmt.Println(v)
			}
		}
	}
}
