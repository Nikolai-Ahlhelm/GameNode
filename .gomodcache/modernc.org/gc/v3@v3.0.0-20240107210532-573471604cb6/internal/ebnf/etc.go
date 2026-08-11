package main

import (
	"bytes"
	"fmt"
	goscanner "go/scanner"
	"go/token"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dustin/go-humanize"
	"golang.org/x/exp/ebnf"
	"modernc.org/mathutil"
	"modernc.org/strutil"
)

const (
	ebnfBudget   = 1e8
	parserBudget = 1e8

	epsilon = -1
)

// The list of tokens.
const (
	// Special tokens
	ILLEGAL = token.ILLEGAL
	EOF     = token.EOF
	COMMENT = token.COMMENT

	// Identifiers and basic type literals
	// (these tokens stand for classes of literals)
	IDENT  = token.IDENT  // main
	INT    = token.INT    // 12345
	FLOAT  = token.FLOAT  // 123.45
	IMAG   = token.IMAG   // 123.45i
	CHAR   = token.CHAR   // 'a'
	STRING = token.STRING // "abc"

	// Operators and delimiters
	ADD = token.ADD // +
	SUB = token.SUB // -
	MUL = token.MUL // *
	QUO = token.QUO // /
	REM = token.REM // %

	AND     = token.AND     // &
	OR      = token.OR      // |
	XOR     = token.XOR     // ^
	SHL     = token.SHL     // <<
	SHR     = token.SHR     // >>
	AND_NOT = token.AND_NOT // &^

	ADD_ASSIGN = token.ADD_ASSIGN // +=
	SUB_ASSIGN = token.SUB_ASSIGN // -=
	MUL_ASSIGN = token.MUL_ASSIGN // *=
	QUO_ASSIGN = token.QUO_ASSIGN // /=
	REM_ASSIGN = token.REM_ASSIGN // %=

	AND_ASSIGN     = token.AND_ASSIGN     // &=
	OR_ASSIGN      = token.OR_ASSIGN      // |=
	XOR_ASSIGN     = token.XOR_ASSIGN     // ^=
	SHL_ASSIGN     = token.SHL_ASSIGN     // <<=
	SHR_ASSIGN     = token.SHR_ASSIGN     // >>=
	AND_NOT_ASSIGN = token.AND_NOT_ASSIGN // &^=

	LAND  = token.LAND  // &&
	LOR   = token.LOR   // ||
	ARROW = token.ARROW // <-
	INC   = token.INC   // ++
	DEC   = token.DEC   // --

	EQL    = token.EQL    // ==
	LSS    = token.LSS    // <
	GTR    = token.GTR    // >
	ASSIGN = token.ASSIGN // =
	NOT    = token.NOT    // !

	NEQ      = token.NEQ      // !=
	LEQ      = token.LEQ      // <=
	GEQ      = token.GEQ      // >=
	DEFINE   = token.DEFINE   // :=
	ELLIPSIS = token.ELLIPSIS // ...

	LPAREN = token.LPAREN // (
	LBRACK = token.LBRACK // [
	LBRACE = token.LBRACE // {
	COMMA  = token.COMMA  // ,
	PERIOD = token.PERIOD // .

	RPAREN    = token.RPAREN    // )
	RBRACK    = token.RBRACK    // ]
	RBRACE    = token.RBRACE    // }
	SEMICOLON = token.SEMICOLON // ;
	COLON     = token.COLON     // :

	// Keywords
	BREAK    = token.BREAK
	CASE     = token.CASE
	CHAN     = token.CHAN
	CONST    = token.CONST
	CONTINUE = token.CONTINUE

	DEFAULT     = token.DEFAULT
	DEFER       = token.DEFER
	ELSE        = token.ELSE
	FALLTHROUGH = token.FALLTHROUGH
	FOR         = token.FOR

	FUNC   = token.FUNC
	GO     = token.GO
	GOTO   = token.GOTO
	IF     = token.IF
	IMPORT = token.IMPORT

	INTERFACE = token.INTERFACE
	MAP       = token.MAP
	PACKAGE   = token.PACKAGE
	RANGE     = token.RANGE
	RETURN    = token.RETURN

	SELECT = token.SELECT
	STRUCT = token.STRUCT
	SWITCH = token.SWITCH
	TYPE   = token.TYPE
	VAR    = token.VAR

	// additional tokens, handled in an ad-hoc manner
	TILDE = token.TILDE
)

var (
	trcTODOs       bool
	extendedErrors = true

	toks = map[string]token.Token{
		"!":             NOT,
		"!=":            NEQ,
		"%":             REM,
		"%=":            REM_ASSIGN,
		"&":             AND,
		"&&":            LAND,
		"&=":            AND_ASSIGN,
		"&^":            AND_NOT,
		"&^=":           AND_NOT_ASSIGN,
		"(":             LPAREN,
		")":             RPAREN,
		"*":             MUL,
		"*=":            MUL_ASSIGN,
		"+":             ADD,
		"++":            INC,
		"+=":            ADD_ASSIGN,
		",":             COMMA,
		"-":             SUB,
		"--":            DEC,
		"-=":            SUB_ASSIGN,
		".":             PERIOD,
		"...":           ELLIPSIS,
		"/":             QUO,
		"/=":            QUO_ASSIGN,
		":":             COLON,
		":=":            DEFINE,
		";":             SEMICOLON,
		"<":             LSS,
		"<-":            ARROW,
		"<<":            SHL,
		"<<=":           SHL_ASSIGN,
		"<=":            LEQ,
		"=":             ASSIGN,
		"==":            EQL,
		">":             GTR,
		">=":            GEQ,
		">>":            SHR,
		">>=":           SHR_ASSIGN,
		"[":             LBRACK,
		"]":             RBRACK,
		"^":             XOR,
		"^=":            XOR_ASSIGN,
		"break":         BREAK,
		"case":          CASE,
		"chan":          CHAN,
		"const":         CONST,
		"continue":      CONTINUE,
		"default":       DEFAULT,
		"defer":         DEFER,
		"else":          ELSE,
		"fallthrough":   FALLTHROUGH,
		"float_lit":     FLOAT,
		"for":           FOR,
		"func":          FUNC,
		"go":            GO,
		"goto":          GOTO,
		"identifier":    IDENT,
		"if":            IF,
		"imaginary_lit": IMAG,
		"import":        IMPORT,
		"int_lit":       INT,
		"interface":     INTERFACE,
		"map":           MAP,
		"package":       PACKAGE,
		"range":         RANGE,
		"return":        RETURN,
		"rune_lit":      CHAR,
		"select":        SELECT,
		"string_lit":    STRING,
		"struct":        STRUCT,
		"switch":        SWITCH,
		"type":          TYPE,
		"var":           VAR,
		"{":             LBRACE,
		"|":             OR,
		"|=":            OR_ASSIGN,
		"||":            LOR,
		"}":             RBRACE,
		"~":             TILDE,
	}
)

type data struct {
	line  int
	cases int
	cnt   int
}

type analyzer struct {
	sync.Mutex
	m map[int]*data // line: data
}

func newAnalyzer() *analyzer {
	return &analyzer{m: map[int]*data{}}
}

func (a *analyzer) record(line, cnt int) {
	d := a.m[line]
	if d == nil {
		d = &data{line: line}
		a.m[line] = d
	}
	d.cases++
	d.cnt += cnt
}

func (a *analyzer) merge(b *analyzer) {
	a.Lock()
	defer a.Unlock()

	for k, v := range b.m {
		d := a.m[k]
		if d == nil {
			d = &data{line: k}
			a.m[k] = d
		}
		d.cases += v.cases
		d.cnt += v.cnt
	}
}

func (a *analyzer) report() string {
	var rows []*data
	for _, v := range a.m {
		rows = append(rows, v)
	}
	sort.Slice(rows, func(i, j int) bool {
		a := rows[i]
		b := rows[j]
		if a.cases < b.cases {
			return true
		}

		if a.cases > b.cases {
			return false
		}

		// a.cases == b.cases
		if a.cnt < b.cnt {
			return true
		}

		if a.cnt > b.cnt {
			return false
		}

		// a.cnt == b.cnt
		return a.line < b.line
	})
	var b strings.Builder
	var cases, cnt int
	for _, row := range rows {
		cases += row.cases
		cnt += row.cnt
		avg := float64(row.cnt) / float64(row.cases)
		fmt.Fprintf(&b, "parser.go:%d:\t%16s %16s %8.1f\n", row.line, h(row.cases), h(row.cnt), avg)
	}
	avg := float64(cnt) / float64(cases)
	fmt.Fprintf(&b, "<total>\t\t%16s %16s %8.1f\n", h(cases), h(cnt), avg)
	return b.String()
}

// origin returns caller's short position, skipping skip frames.
func origin(skip int) string {
	pc, fn, fl, _ := runtime.Caller(skip)
	f := runtime.FuncForPC(pc)
	var fns string
	if f != nil {
		fns = f.Name()
		if x := strings.LastIndex(fns, "."); x > 0 {
			fns = fns[x+1:]
		}
		if strings.HasPrefix(fns, "func") {
			num := true
			for _, c := range fns[len("func"):] {
				if c < '0' || c > '9' {
					num = false
					break
				}
			}
			if num {
				return origin(skip + 2)
			}
		}
	}
	return fmt.Sprintf("%s:%d:%s", filepath.Base(fn), fl, fns)
}

// todo prints and returns caller's position and an optional message tagged with TODO. Output goes to stderr.
func todo(s string, args ...interface{}) string {
	switch {
	case s == "":
		s = fmt.Sprintf(strings.Repeat("%v ", len(args)), args...)
	default:
		s = fmt.Sprintf(s, args...)
	}
	r := fmt.Sprintf("%s\n\tTODO %s", origin(2), s)
	// fmt.Fprintf(os.Stderr, "%s\n", r)
	// os.Stdout.Sync()
	return r
}

// trc prints and returns caller's position and an optional message tagged with TRC. Output goes to stderr.
func trc(s string, args ...interface{}) string {
	switch {
	case s == "":
		s = fmt.Sprintf(strings.Repeat("%v ", len(args)), args...)
	default:
		s = fmt.Sprintf(s, args...)
	}
	r := fmt.Sprintf("%s: TRC %s", origin(2), s)
	fmt.Fprintf(os.Stderr, "%s\n", r)
	os.Stderr.Sync()
	return r
}

func unexport(s string) (r string) {
	switch r = strings.ToLower(s[:1]) + s[1:]; r {
	case "type":
		return "type1"
	default:
		return r
	}
}

func extractPos(s string) (p token.Position, ok bool) {
	var prefix string
	if len(s) > 1 && s[1] == ':' { // c:\foo
		prefix = s[:2]
		s = s[2:]
	}
	// "testdata/parser/bug/001.c:1193: ..."
	a := strings.Split(s, ":")
	// ["testdata/parser/bug/001.c" "1193" "..."]
	if len(a) < 2 {
		return p, false
	}

	line, err := strconv.Atoi(a[1])
	if err != nil {
		return p, false
	}

	col, err := strconv.Atoi(a[2])
	if err != nil {
		col = 1
	}

	return token.Position{Filename: prefix + a[0], Line: line, Column: col}, true
}

func ebnfString(e ebnf.Expression) string {
	var b strings.Builder
	printEBNFExpression(&b, e)
	return b.String()
}

func printEBNFExpression(w io.Writer, e ebnf.Expression) {
	switch x := e.(type) {
	case ebnf.Sequence:
		for i, v := range x {
			if i != 0 {
				fmt.Fprintf(w, " ")
			}
			printEBNFExpression(w, v)
		}
	case *ebnf.Name:
		fmt.Fprintf(w, "%s", x.String)
	case *ebnf.Token:
		s := x.String
		fmt.Fprintf(w, "%q", s)
	case *ebnf.Option:
		fmt.Fprintf(w, "[ ")
		printEBNFExpression(w, x.Body)
		fmt.Fprintf(w, " ]")
	case *ebnf.Group:
		fmt.Fprintf(w, "( ")
		printEBNFExpression(w, x.Body)
		fmt.Fprintf(w, " )")
	case ebnf.Alternative:
		for i, v := range x {
			if i != 0 {
				fmt.Fprintf(w, " | ")
			}
			printEBNFExpression(w, v)
		}
	case *ebnf.Repetition:
		fmt.Fprintf(w, "{ ")
		printEBNFExpression(w, x.Body)
		fmt.Fprintf(w, " }")
	case *ebnf.Range:
		printEBNFExpression(w, x.Begin)
		fmt.Fprintf(w, " ... ")
		printEBNFExpression(w, x.End)
	case nil:
		// ok
	default:
		panic(todo("%T", x))
	}
}

// errorf constructs an error value. If ExtendedErrors is true, the error will
// contain its origin.
func errorf(s string, args ...interface{}) error {
	switch {
	case s == "":
		s = fmt.Sprintf(strings.Repeat("%v ", len(args)), args...)
	default:
		s = fmt.Sprintf(s, args...)
	}
	if trcTODOs && strings.HasPrefix(s, "TODO") {
		fmt.Fprintf(os.Stderr, "%s (%v)\n", s, origin(2))
		os.Stderr.Sync()
	}
	switch {
	case extendedErrors:
		return fmt.Errorf("%s (%v: %v: %v)", s, origin(4), origin(3), origin(2))
	default:
		return fmt.Errorf("%s", s)
	}
}

func h(v interface{}) string {
	switch x := v.(type) {
	case int:
		return humanize.Comma(int64(x))
	case int32:
		return humanize.Comma(int64(x))
	case int64:
		return humanize.Comma(x)
	case uint32:
		return humanize.Comma(int64(x))
	case uint64:
		if x <= math.MaxInt64 {
			return humanize.Comma(int64(x))
		}
	}
	return fmt.Sprint(v)
}

type parallel struct {
	a                  *analyzer
	asts               []interface{}
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

	allToks int32
	fails   int32
	files   int32
	ok      int32
	skipped int32
	stopped int32

	maxBacktrack      int
	maxBacktrackToks  int
	maxBacktracks     int
	maxBacktracksToks int
	maxBudget         int
	maxBudgetToks     int
	maxDurationToks   int
	minToks           int
}

func newParallel() *parallel {
	return &parallel{
		a:     newAnalyzer(),
		limit: make(chan struct{}, runtime.GOMAXPROCS(0)),
	}
}

func (p *parallel) addFail()      { atomic.AddInt32(&p.fails, 1) }
func (p *parallel) addFile()      { atomic.AddInt32(&p.files, 1) }
func (p *parallel) addOk()        { atomic.AddInt32(&p.ok, 1) }
func (p *parallel) addSkipped()   { atomic.AddInt32(&p.skipped, 1) }
func (p *parallel) addToks(n int) { atomic.AddInt32(&p.allToks, int32(n)) }
func (p *parallel) stop()         { atomic.StoreInt32(&p.stopped, 1) }

func (p *parallel) addAST(ast interface{}) {
	p.Lock()
	defer p.Unlock()

	p.asts = append(p.asts, ast)
}

func (p *parallel) recordMaxDuration(path string, d time.Duration, toks int) {
	p.Lock()
	defer p.Unlock()

	if d > p.maxDuration {
		p.maxDuration = d
		p.maxDurationPath = path
		p.maxDurationToks = toks
	}
}

func (p *parallel) recordMaxBacktrack(path string, back, toks int, pos, origin string) {
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

func (p *parallel) recordMaxBack(path string, back, toks int) {
	p.Lock()
	defer p.Unlock()

	if back > p.maxBacktracks {
		p.maxBacktracks = back
		p.maxBacktracksPath = path
		p.maxBacktracksToks = toks
	}
}

func (p *parallel) recordMaxBudget(path string, budget, toks int) {
	p.Lock()
	defer p.Unlock()

	if budget > p.maxBudget {
		p.maxBudget = budget
		p.maxBudgetToks = toks
		p.maxBudgetPath = path
	}
}

func (p *parallel) recordMinToks(path string, toks int) {
	p.Lock()
	defer p.Unlock()

	if p.minToks == 0 || toks < p.minToks {
		p.minToks = toks
		p.minToksPath = path
	}
}

func (p *parallel) err(err error) {
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

func (p *parallel) exec(run func() error) {
	if atomic.LoadInt32(&p.stopped) != 0 {
		return
	}

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

func (p *parallel) wait() error {
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

type followSet map[token.Token]struct{}

func (fs followSet) has(t token.Token) (r bool) { _, r = fs[t]; return r }

func (fs followSet) eq(d followSet) (r bool) {
	if len(fs) != len(d) {
		return false
	}

	for k := range fs {
		if _, r := d[k]; !r {
			return r
		}
	}
	return true
}

func (fs followSet) intersect(d followSet) (r followSet) {
	if len(fs) == 0 || len(d) == 0 {
		return nil
	}
	r = followSet{}
	for k := range fs {
		if _, ok := d[k]; ok {
			r[k] = struct{}{}
		}
	}
	return r
}

func (fs followSet) isSubsetOf(d followSet) (r bool) {
	if len(d) < len(fs) {
		return false
	}

	for k := range fs {
		if _, r := d[k]; !r {
			return r
		}
	}
	return true
}

func (fs followSet) clone() (r followSet) {
	if fs == nil {
		return nil
	}

	r = make(followSet, len(fs))
	for k := range fs {
		r[k] = struct{}{}
	}
	return r
}

func (fs followSet) caseStr() string {
	var a []string
	var e string
	for k := range fs {
		switch k {
		case epsilon:
			e = " /* ε */"
		default:
			a = append(a, tokSource(k))
		}
	}
	sort.Strings(a)
	return strings.Join(a, ", ") + e
}

func (fs followSet) hasEpsilon() (r bool) { _, r = fs[epsilon]; return r }

func (fs *followSet) add(t token.Token) followSet {
	m := *fs
	if m == nil {
		m = followSet{}
		*fs = m
	}
	m[t] = struct{}{}
	return m
}

func (fs *followSet) union(d followSet) followSet {
	m := *fs
	if d == nil {
		return m
	}

	if m == nil {
		m = followSet{}
		*fs = m
	}
	for k, v := range d {
		m[k] = v
	}
	return m
}

type closure map[string]ebnf.Expression

func (c *closure) union(d closure) closure {
	m := *c
	if d == nil {
		return m
	}

	if m == nil {
		m = closure{}
		*c = m
	}
	for k, v := range d {
		m[k] = v
	}
	return m
}

func (c closure) String() string {
	var a []string
	for k := range c {
		a = append(a, k)
	}
	sort.Slice(a, func(i, j int) bool { return lessString(a[i], a[j]) })
	return strings.Join(a, " ")
}

type grammar struct {
	expressionClosures   map[string]closure
	expressionFollowSets map[string]followSet
	g                    ebnf.Grammar
	leftRecursive        map[string]struct{}
	productionClosures   map[*ebnf.Production]closure
	productionFollowSets map[*ebnf.Production]followSet
}

func newGrammar(name, start string, src []byte) (r *grammar, err error) {
	g, err := ebnf.Parse(name, bytes.NewBuffer(src))
	if err != nil {
		return nil, err
	}

	if err = ebnf.Verify(g, start); err != nil {
		return nil, err
	}

	r = &grammar{
		expressionClosures:   map[string]closure{},
		expressionFollowSets: map[string]followSet{},
		g:                    g,
		leftRecursive:        map[string]struct{}{},
		productionClosures:   map[*ebnf.Production]closure{},
		productionFollowSets: map[*ebnf.Production]followSet{},
	}
	for nm, p := range r.g {
		if token.IsExported(nm) {
			var fs followSet
			e := p.Expr
			switch {
			case e == nil:
				fs.add(epsilon)
			default:
				fs = r.followSet0(nm, e, map[string]struct{}{})
			}
			r.expressionFollowSets[ebnfString(p.Expr)] = fs
			r.productionFollowSets[p] = fs
			r.productionClosures[p] = r.closure(p.Expr)
		}
	}
	return r, nil
}

func (p *grammar) tok(s string) token.Token {
	if r, ok := toks[s]; ok {
		return r
	}

	panic(todo("%q", s))
}

func (g *grammar) closure(e ebnf.Expression) (r closure) {
	k := ebnfString(e)
	if r, ok := g.expressionClosures[k]; ok {
		return r
	}

	r = g.closure0(e, map[string]struct{}{})
	g.expressionClosures[k] = r
	return r
}

func (g *grammar) closure0(e ebnf.Expression, m map[string]struct{}) (r closure) {
	k := ebnfString(e)
	if _, ok := m[k]; ok {
		return nil
	}

	m[k] = struct{}{}
	r = closure{}
	switch x := e.(type) {
	case ebnf.Alternative:
		for _, v := range x {
			r.union(g.closure0(v, m))
		}
		return r
	case *ebnf.Group:
		return g.closure0(x.Body, m)
	case *ebnf.Name:
		nm := x.String
		p := g.g[nm]
		e := p.Expr
		r[nm] = x
		if token.IsExported(nm) {
			r.union(g.closure0(e, m))
		}
		return r
	case *ebnf.Option:
		r[""] = nil
		return r.union(g.closure0(x.Body, m))
	case *ebnf.Repetition:
		r[""] = nil
		return r.union(g.closure0(x.Body, m))
	case ebnf.Sequence:
		for _, v := range x {
			s := g.closure0(v, m)
			r.union(s)
			if _, ok := s[""]; !ok {
				delete(r, "")
				break
			}
		}
		return r
	case *ebnf.Token:
		r[k] = x
		return r
	case nil:
		r[k] = nil
		return r
	}
	panic(todo("%T %s", e, k))
}

func (g *grammar) followSet(e ebnf.Expression) (r followSet) {
	if e == nil {
		panic(todo(""))
	}

	k := ebnfString(e)
	if r, ok := g.expressionFollowSets[k]; ok {
		return r
	}

	r = g.followSet0("", e, map[string]struct{}{})
	g.expressionFollowSets[k] = r
	return r
}

func (g *grammar) followSet0(prod string, e ebnf.Expression, m map[string]struct{}) (r followSet) {
	if e == nil {
		panic(todo(""))
	}

	k := ebnfString(e)
	if _, ok := m[k]; ok {
		return nil
	}

	m[k] = struct{}{}
	r = followSet{}
	switch x := e.(type) {
	case ebnf.Alternative:
		for _, v := range x {
			r.union(g.followSet0(prod, v, m))
		}
		return r
	case *ebnf.Group:
		return g.followSet0(prod, x.Body, m)
	case *ebnf.Name:
		nm := x.String
		if nm == prod {
			g.leftRecursive[nm] = struct{}{}
		}
		p := g.g[nm]
		e := p.Expr
		if token.IsExported(nm) {
			if e == nil {
				return r.add(epsilon)
			}

			return r.union(g.followSet0(prod, e, m))
		}

		if x, ok := toks[nm]; ok {
			r.add(x)
		}
		return r
	case *ebnf.Option:
		r.add(epsilon)
		return r.union(g.followSet0(prod, x.Body, m))
	case *ebnf.Repetition:
		r.add(epsilon)
		return r.union(g.followSet0(prod, x.Body, m))
	case ebnf.Sequence:
		for _, v := range x {
			s := g.followSet0(prod, v, m)
			r.union(s)
			if _, ok := s[epsilon]; !ok {
				delete(r, epsilon)
				break
			}
		}
		return r
	case *ebnf.Token:
		return r.add(g.tok(x.String))
	}
	panic(todo("%q %T %s", prod, e, k))
}

type ebnfTok struct {
	f   *token.File
	pos token.Pos
	tok token.Token
	lit string
}

func (n ebnfTok) Position() token.Position { return n.f.PositionFor(n.pos, true) }

func (t ebnfTok) String() string {
	lit := t.lit
	if lit == "" {
		lit = fmt.Sprint(t.tok)
	}
	return fmt.Sprintf("%d: %v %q", t.pos, t.tok, lit)
}

func pos(p token.Position) token.Position {
	p.Filename = ""
	return p
}

func tokSource(t token.Token) string {
	if t == epsilon {
		return "ε"
	}

	switch t {
	case ILLEGAL:
		return "ILLEGAL"
	case EOF:
		return "EOF"
	case COMMENT:
		return "COMMENT"
	case IDENT:
		return "IDENT"
	case INT:
		return "INT"
	case FLOAT:
		return "FLOAT"
	case IMAG:
		return "IMAG"
	case CHAR:
		return "CHAR"
	case STRING:
		return "STRING"
	case ADD:
		return "ADD"
	case SUB:
		return "SUB"
	case MUL:
		return "MUL"
	case QUO:
		return "QUO"
	case REM:
		return "REM"
	case AND:
		return "AND"
	case OR:
		return "OR"
	case XOR:
		return "XOR"
	case SHL:
		return "SHL"
	case SHR:
		return "SHR"
	case AND_NOT:
		return "AND_NOT"
	case ADD_ASSIGN:
		return "ADD_ASSIGN"
	case SUB_ASSIGN:
		return "SUB_ASSIGN"
	case MUL_ASSIGN:
		return "MUL_ASSIGN"
	case QUO_ASSIGN:
		return "QUO_ASSIGN"
	case REM_ASSIGN:
		return "REM_ASSIGN"
	case AND_ASSIGN:
		return "AND_ASSIGN"
	case OR_ASSIGN:
		return "OR_ASSIGN"
	case XOR_ASSIGN:
		return "XOR_ASSIGN"
	case SHL_ASSIGN:
		return "SHL_ASSIGN"
	case SHR_ASSIGN:
		return "SHR_ASSIGN"
	case AND_NOT_ASSIGN:
		return "AND_NOT_ASSIGN"
	case LAND:
		return "LAND"
	case LOR:
		return "LOR"
	case ARROW:
		return "ARROW"
	case INC:
		return "INC"
	case DEC:
		return "DEC"
	case EQL:
		return "EQL"
	case LSS:
		return "LSS"
	case GTR:
		return "GTR"
	case ASSIGN:
		return "ASSIGN"
	case NOT:
		return "NOT"
	case NEQ:
		return "NEQ"
	case LEQ:
		return "LEQ"
	case GEQ:
		return "GEQ"
	case DEFINE:
		return "DEFINE"
	case ELLIPSIS:
		return "ELLIPSIS"
	case LPAREN:
		return "LPAREN"
	case LBRACK:
		return "LBRACK"
	case LBRACE:
		return "LBRACE"
	case COMMA:
		return "COMMA"
	case PERIOD:
		return "PERIOD"
	case RPAREN:
		return "RPAREN"
	case RBRACK:
		return "RBRACK"
	case RBRACE:
		return "RBRACE"
	case SEMICOLON:
		return "SEMICOLON"
	case COLON:
		return "COLON"
	case BREAK:
		return "BREAK"
	case CASE:
		return "CASE"
	case CHAN:
		return "CHAN"
	case CONST:
		return "CONST"
	case CONTINUE:
		return "CONTINUE"
	case DEFAULT:
		return "DEFAULT"
	case DEFER:
		return "DEFER"
	case ELSE:
		return "ELSE"
	case FALLTHROUGH:
		return "FALLTHROUGH"
	case FOR:
		return "FOR"
	case FUNC:
		return "FUNC"
	case GO:
		return "GO"
	case GOTO:
		return "GOTO"
	case IF:
		return "IF"
	case IMPORT:
		return "IMPORT"
	case INTERFACE:
		return "INTERFACE"
	case MAP:
		return "MAP"
	case PACKAGE:
		return "PACKAGE"
	case RANGE:
		return "RANGE"
	case RETURN:
		return "RETURN"
	case SELECT:
		return "SELECT"
	case STRUCT:
		return "STRUCT"
	case SWITCH:
		return "SWITCH"
	case TYPE:
		return "TYPE"
	case VAR:
		return "VAR"
	case TILDE:
		return "TILDE"
	default:
		panic(todo("", int(t), t))
	}
}

type ebnfParser struct {
	f             *token.File
	g             *grammar
	maxBackOrigin string
	maxBackRange  [2]int
	path          string
	toks          []ebnfTok

	backs    int
	budget   int
	indentN  int
	index    int
	maxBack  int
	maxIndex int

	trcPEG bool
}

func newEBNFParser(g *grammar, path string, src []byte, trcPEG bool) (r *ebnfParser, err error) {
	r = &ebnfParser{
		budget: ebnfBudget,
		g:      g,
		path:   path,
		trcPEG: trcPEG,
	}
	var s goscanner.Scanner
	fs := token.NewFileSet()
	f := fs.AddFile(path, -1, len(src))
	r.f = f
	s.Init(r.f, src, func(pos token.Position, msg string) { err = errorf("%v: %s", pos, msg) }, 0)
	for {
		pos, t, lit := s.Scan()
		r.toks = append(r.toks, ebnfTok{f, pos, t, lit})
		if err != nil {
			return nil, err
		}

		if t == token.EOF {
			return r, nil
		}
	}
}

func (p *ebnfParser) pos() token.Position { return p.toks[p.index].Position() }

func (p *ebnfParser) indent() (r string) {
	p.indentN++
	return fmt.Sprintf("%5d: ", p.indentN-1) + strings.Repeat("· ", p.indentN-1)
}

func (p *ebnfParser) undent() string {
	p.indentN--
	return fmt.Sprintf("%5d: ", p.indentN) + strings.Repeat("· ", p.indentN)
}

func (p *ebnfParser) tok(s string) token.Token {
	if r, ok := toks[s]; ok {
		return r
	}

	panic(todo("%q", s))
}

func (p *ebnfParser) c() ebnfTok {
	if p.budget == 0 {
		return p.toks[len(p.toks)-1]
	}

	p.maxIndex = mathutil.Max(p.maxIndex, p.index)
	return p.toks[p.index]
}

func (p *ebnfParser) accept(b bool) bool {
	if b {
		p.index++
		p.budget--
	}
	return b
}

func (p *ebnfParser) parse(start string) error {
	ok := p.parseExpression(p.g.g[start].Expr)
	if p.budget == 0 {
		return errorf("%s: resources exhausted", p.errPosition())
	}

	if !ok || p.index < len(p.toks)-1 {
		return errorf("%s: syntax error", p.errPosition())
	}

	return nil
}

func (p *ebnfParser) recordBacktrack(index int) {
	delta := p.index - index
	p.backs += delta
	if delta > p.maxBack {
		p.maxBack = delta
		p.maxBackRange = [2]int{index, p.index}
		p.maxBackOrigin = origin(3)
	}
	p.index = index
}

func (p *ebnfParser) errPosition() token.Position {
	return p.f.PositionFor(p.toks[p.maxIndex].pos, true)
}

func (p *ebnfParser) parseExpression(e ebnf.Expression) (r bool) {
	// trc("%s%v: %v '%s'", p.indent(), p.pos(), p.c(), ebnfString(e))
	// defer func() {
	// 	s := "REJ"
	// 	if r {
	// 		s = "ACC"
	// 	}
	// 	trc("%s%s: %v: '%s'", p.undent(), s, p.pos(), ebnfString(e))
	// }()
	index0 := p.index
out:
	switch x := e.(type) {
	case ebnf.Sequence:
		for _, v := range x {
			if !p.parseExpression(v) {
				break out
			}
		}
		return true
	case *ebnf.Name:
		switch nm := x.String; {
		case token.IsExported(nm):
			pr := p.g.g[nm]
			fs := p.g.productionFollowSets[pr]
			if _, ok := fs[p.c().tok]; ok {
				if p.parseExpression(pr.Expr) {
					return true
				}

				break out
			}

			if fs.hasEpsilon() {
				return true
			}
		default:
			if p.accept(p.c().tok == p.tok(x.String)) {
				return true
			}
		}
	case *ebnf.Token:
		if p.accept(p.c().tok == p.tok(x.String)) {
			return true
		}
	case *ebnf.Repetition:
		for {
			if !p.parseExpression(x.Body) {
				return true
			}
		}
	case *ebnf.Group:
		if p.parseExpression(x.Body) {
			return true
		}
	case ebnf.Alternative:
		for _, v := range x {
			if p.parseExpression(v) {
				return true
			}
		}
	case *ebnf.Option:
		p.parseExpression(x.Body)
		return true
	default:
		panic(todo("%T", x))
	}
	p.recordBacktrack(index0)
	return false
}

type parser struct {
	a             *analyzer
	f             *token.File
	maxBackOrigin string
	maxBackRange  [2]int
	path          string
	s             *scanner

	backs         int
	budget        int
	ix            int
	maxBack       int
	maxBudgetToks int
	maxIx         int

	closed bool
	record bool
}

func newParser(path string, src []byte, record bool) (r *parser, err error) {
	s := newScanner(path, src)
	for s.scan() {
	}
	if err = s.errs.Err(); err != nil {
		return nil, err
	}

	r = &parser{
		a:      newAnalyzer(),
		budget: parserBudget,
		path:   path,
		record: record,
		s:      s,
	}
	return r, nil
}

func (p *parser) errPosition() token.Position { return p.s.toks[p.maxIx].position(p.s.source) }

func (p *parser) c() token.Token { return p.peek(0) }

func (p *parser) accept(t token.Token) (r Token, ok bool) {
	if p.c() == t {
		r = Token{p.s.source, p.s.toks[p.ix].ch, int32(p.ix)}
		p.ix++
		p.budget--
		return r, true
	}

	return r, false
}

func (p *parser) expect(t token.Token) (r Token) {
	r, ok := p.accept(t)
	if !ok {
		p.closed = true
	}
	return r
}

func (p *parser) peek(n int) token.Token {
	if p.budget <= 0 {
		return p.s.toks[len(p.s.toks)-1].token()
	}

	p.maxIx = mathutil.Max(p.maxIx, p.ix+n)
	return p.s.toks[p.ix+n].token()
}

func (p *parser) recordBacktrack(ix int) {
	delta := p.ix - ix
	p.backs += delta
	if delta > p.maxBack {
		p.maxBack = delta
		p.maxBackRange = [2]int{ix, p.ix}
		p.maxBackOrigin = origin(3)
	}
	p.ix = ix
	if p.record {
		if _, _, line, ok := runtime.Caller(2); ok {
			p.a.record(line, delta)
		}
	}
}

func (p *parser) back(ix int) {
	p.recordBacktrack(ix)
}

func (p *parser) parse() (ast *AST, err error) {
	if p.c() != PACKAGE {
		return nil, errorf("%s: syntax error", p.errPosition())
	}

	sourceFile := p.sourceFile()
	if p.budget == 0 {
		return nil, errorf("%s: resources exhausted", p.path)
	}

	if eof, ok := p.accept(EOF); ok {
		return &AST{sourceFile, eof}, nil
	}

	return nil, errorf("%s: syntax error", p.errPosition())
}

type AST struct {
	SourceFile *SourceFileNode
	EOF        Token
}

func (n *AST) Position() token.Position { return n.SourceFile.Position() }
func (n *AST) Source(full bool) string  { return nodeSource(n, full) }

func ints(s string) (r []int) {
	a := strings.Split(s, " ")
	for _, v := range a {
		n, err := strconv.Atoi(v)
		if err != nil {
			panic(todo("", v))
		}

		r = append(r, n)
	}
	return r
}

func loadPEG(fn string) (*grammar, error) {
	b, err := os.ReadFile(fn)
	if err != nil {
		return nil, err
	}

	return newGrammar(fn, startProduction, b)
}

func lessString(a, b string) bool {
	switch {
	case a[0] >= 'a' && a[0] <= 'z':
		switch {
		case b[0] >= 'A' && b[0] <= 'Z':
			return true
		}
	case a[0] >= 'A' && a[0] <= 'Z':
		switch {
		case b[0] >= 'a' && b[0] <= 'z':
			return false
		}
	}
	return a < b
}

// Node is an item of the CST tree.
type Node interface {
	Position() token.Position
	Source(full bool) string
}

// nodeSource returns the source text of n. If full is false, every non empty
// separator is replaced by a single space.
func nodeSource(n Node, full bool) string {
	var a []int32
	var t Token
	nodeSource0(&t.source, &a, n)
	if len(a) == 0 {
		return ""
	}

	var b strings.Builder
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	for _, v := range a {
		t.index = v
		t.ch = t.source.toks[t.index].ch
		b.WriteString(t.Source(full))
	}
	return b.String()
}

func nodeSource0(ps **source, a *[]int32, n interface{}) {
	if n == nil {
		return
	}

	if t, ok := n.(Token); ok {
		if t.IsValid() {
			*ps = t.source
			*a = append(*a, t.index)
		}
		return
	}

	t := reflect.TypeOf(n)
	v := reflect.ValueOf(n)
	switch t.Kind() {
	case reflect.Pointer:
		if !v.IsZero() {
			nodeSource0(ps, a, v.Elem().Interface())
		}
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			nodeSource0(ps, a, v.Field(i).Interface())
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			nodeSource0(ps, a, v.Index(i).Interface())
		}
	default:
		panic(todo("", t.Kind()))
	}
}

var hooks = strutil.PrettyPrintHooks{
	reflect.TypeOf(Token{}): func(f strutil.Formatter, v interface{}, prefix, suffix string) {
		t := v.(Token)
		if !t.IsValid() {
			return
		}

		pos := t.Position()
		if pos.Filename != "" {
			pos.Filename = filepath.Base(pos.Filename)
		}
		f.Format(string(prefix)+"%v: %10s %q %q"+string(suffix), pos, tokSource(t.Ch()), t.Sep(), t.Src())
	},
}

func dump(n Node) string { return strutil.PrettyString(n, "", "", hooks) }
