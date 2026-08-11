package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	goparser "go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	defaultSrc      = "../.."
	pegEBNF         = "peg.ebnf"
	startProduction = "SourceFile"
)

var (
	oAssert = flag.Bool("assert", false, "verify some invariants in the generated parser")
	oBSrc   = flag.String("bsrc", runtime.GOROOT(), "")
	oGen    = flag.Bool("gen", false, "")
	oHeap   = flag.Bool("heap", false, "")
	oRE     = flag.String("re", "", "")
	oReport = flag.Bool("report", false, "")
	oSrc    = flag.String("src", defaultSrc, "")
	oTrc    = flag.Bool("trc", false, "")
	oTrcPEG = flag.Bool("trcpeg", false, "")

	re *regexp.Regexp
)

func TestMain(m *testing.M) {
	flag.BoolVar(&trcTODOs, "trctodo", false, "")
	flag.Parse()
	if s := *oRE; s != "" {
		re = regexp.MustCompile(s)
	}

	os.Exit(m.Run())
}

func TestSpecEBNF(t *testing.T) {
	b, _, err := verifySpecEBNF(filepath.Join(runtime.GOROOT(), "doc", "go_spec.html"), startProduction, nil)
	if err != nil {
		t.Fatal(err)
	}

	s := strings.ReplaceAll(string(b), "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&amp;", "&")
	g, err := newGrammar("spec", startProduction, []byte(s))
	if err != nil {
		t.Fatal(err)
	}

	for k := range g.leftRecursive {
		t.Logf("left recursive: %v", k)
	}
}

func TestPEGEBNF(t *testing.T) {
	testGrammar(t, pegEBNF)
}

func testGrammar(t *testing.T, fn string) {
	peg, err := loadPEG(fn)
	if err != nil {
		t.Fatal(err)
	}

	for k := range peg.leftRecursive {
		t.Errorf("left recursive: %v", k)
	}

	var a, b []string
	for k := range peg.g {
		if token.IsExported(k) {
			a = append(a, k)
		}
	}
	sort.Slice(a, func(i, j int) bool {
		x := a[i]
		y := a[j]
		u := noPreBlock(x)
		v := noPreBlock(y)
		if lessString(u, v) {
			return true
		}

		if lessString(v, u) {
			return false
		}

		return lessString(x, y)
	})
	for _, v := range a {
		b = append(b, fmt.Sprintf("%32s = %s", v, peg.productionFollowSets[peg.g[v]].caseStr()))
	}
	if err := os.WriteFile(fn+".fs", []byte(strings.Join(b, "\n")), 0660); err != nil {
		t.Fatal(err)
	}
	for i, v := range a {
		b[i] = fmt.Sprintf("%32s = %s", v, peg.productionClosures[peg.g[v]])
	}
	if err := os.WriteFile(fn+".cls", []byte(strings.Join(b, "\n")), 0660); err != nil {
		t.Fatal(err)
	}
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

func TestEBNFParser(t *testing.T) {
	peg, err := loadPEG(pegEBNF)
	if err != nil {
		t.Fatal(err)
	}

	gld := newGolden(t, "testdata/test_parse.ebnf.golden")

	defer gld.close()

	p := newParallel()
	t.Run("src", func(t *testing.T) { testEBNFParser(p, t, peg, *oSrc, gld) })
	t.Run("goroot", func(t *testing.T) { testEBNFParser(p, t, peg, runtime.GOROOT(), gld) })
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
}

func testEBNFParser(p *parallel, t *testing.T, g *grammar, root string, gld *golden) {
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

			var pp *ebnfParser
			t0 := time.Now()

			defer func() {
				if err != nil {
					p.addFail()
					if pp != nil {
						p.recordMinToks(path, len(pp.toks))
					}
				}
				if pp != nil {
					p.recordMaxDuration(path, time.Since(t0), len(pp.toks))
					from := pp.toks[pp.maxBackRange[0]].Position()
					to := pp.toks[pp.maxBackRange[1]].Position()
					p.recordMaxBacktrack(path, pp.maxBack, len(pp.toks), fmt.Sprintf("%v: - %v:", from, to), pp.maxBackOrigin)
					p.recordMaxBack(path, pp.backs, len(pp.toks))
					p.recordMaxBudget(path, ebnfBudget-pp.budget, len(pp.toks))
				}
			}()

			b, err := os.ReadFile(path)
			if err != nil {
				return errorf("%s: %v", path, err)
			}

			if pp, err = newEBNFParser(g, path, b, *oTrcPEG); err != nil {
				pp = nil
				p.addSkipped()
				return nil
			}

			p.addToks(len(pp.toks))
			if err := pp.parse(startProduction); err != nil {
				if isKnownBad(path, pp.errPosition()) {
					p.addSkipped()
					return nil
				}

				return errorf("%s", err)
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

var falseNegatives = []string{
	"golang.org/x/tools/go/analysis/passes/unreachable/testdata/src/a/a.go",
}

func isKnownBad(fn string, pos token.Position) bool {
	fs := token.NewFileSet()
	ast, err := goparser.ParseFile(fs, fn, nil, goparser.SkipObjectResolution|goparser.ParseComments)
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
	gld := newGolden(t, "testdata/test_parse.golden")

	defer gld.close()

	var ms0, ms runtime.MemStats
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms0)
	p := newParallel()
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
		t.Logf("ast count %v, heap %s", h(len(p.asts)), h(ms.HeapAlloc-ms0.HeapAlloc))
	}
}

func testParser(p *parallel, t *testing.T, root string, gld *golden) {
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
					from := pp.s.toks[pp.maxBackRange[0]].position(pp.s.source)
					to := pp.s.toks[pp.maxBackRange[1]].position(pp.s.source)
					p.recordMaxBacktrack(path, pp.maxBack, len(pp.s.toks), fmt.Sprintf("%v: - %v:", from, to), pp.maxBackOrigin)
					p.recordMaxBack(path, pp.backs, len(pp.s.toks))
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

			if pp, err = newParser(path, b, *oReport); err != nil {
				pp = nil
				p.addSkipped()
				return nil
			}

			p.addToks(len(pp.s.toks))
			ast, err := pp.parse()
			if err != nil {
				if isKnownBad(path, pp.errPosition()) {
					pp = nil
					p.addSkipped()
					return nil
				}

				return errorf("%s", err)
			}

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
				p.addAST(ast)
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

func TestGen(t *testing.T) {
	if !*oGen {
		t.Skip("enable with -gen")
	}

	if err := generate("parser.go", pegEBNF); err != nil {
		t.Fatal(err)
	}
}

func TestGoParser(t *testing.T) {
	gld := newGolden(t, "testdata/test_parse.go.golden")

	defer gld.close()

	var ms0, ms runtime.MemStats
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms0)
	p := newParallel()
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
		t.Logf("ast count %v, heap %s", h(len(p.asts)), h(ms.HeapAlloc-ms0.HeapAlloc))
	}
}

func testGoParser(p *parallel, t *testing.T, root string, gld *golden) {
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

				p.recordMaxDuration(path, time.Since(t0), -1)
			}()

			b, err := os.ReadFile(path)
			if err != nil {
				return errorf("%s: %v", path, err)
			}

			ast, err := goparser.ParseFile(token.NewFileSet(), path, b, goparser.SkipObjectResolution)
			if err != nil {
				if pos, ok := extractPos(err.Error()); !ok || isKnownBad(path, pos) {
					p.addSkipped()
					return nil
				}

				return errorf("%s", err)
			}

			if *oHeap && *oSrc == defaultSrc {
				p.addAST(ast)
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

				if pp, err = newParser(path, b, *oReport); err != nil {
					return nil
				}

				if _, err := pp.parse(); err != nil {
					if isKnownBad(path, pp.errPosition()) {
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

				if _, err = goparser.ParseFile(token.NewFileSet(), path, b, goparser.SkipObjectResolution); err != nil {
					if pos, ok := extractPos(err.Error()); !ok || isKnownBad(path, pos) {
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
