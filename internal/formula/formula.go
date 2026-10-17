// Package formula evaluates spreadsheet-like arithmetic expressions for custom statistics
// columns, e.g. "kills / max(deaths, 1)".
//
// Syntax: numbers, variables, + - * / % ^ (power), comparisons (< <= > >= == !=), && || !,
// parentheses and functions: abs, sqrt, exp, ln, log (base 10, or log(x, base)), log10,
// floor, ceil, round (round(x) or round(x, digits)), min, max (any number of arguments),
// pow and if(condition, then, else). Names are case-insensitive. Comparisons and logic yield
// 1 or 0; any non-zero value is true.
package formula

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Limits keep user formulas cheap to evaluate.
const (
	MaxLength = 1000
	maxDepth  = 64
)

// Expr is a compiled formula.
type Expr struct {
	root node
	vars []string
}

// Vars returns the variables the formula uses (lower case).
func (e *Expr) Vars() []string { return e.vars }

// Eval evaluates the formula. Missing variables count as 0. The result may be NaN or ±Inf
// (for example after a division by zero); callers decide how to present those.
func (e *Expr) Eval(vars map[string]float64) float64 {
	return e.root.eval(vars)
}

// Error is a compile error with the byte offset it refers to.
type Error struct {
	Pos int
	Msg string
}

func (e *Error) Error() string { return fmt.Sprintf("position %d: %s", e.Pos+1, e.Msg) }

// Compile parses a formula. When known is non-nil, unknown variable names are rejected.
func Compile(src string, known []string) (*Expr, error) {
	if strings.TrimSpace(src) == "" {
		return nil, &Error{0, "empty formula"}
	}
	if len(src) > MaxLength {
		return nil, &Error{MaxLength, fmt.Sprintf("formula longer than %d characters", MaxLength)}
	}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, known: known}
	root, err := p.parseExpr(0)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, &Error{t.pos, fmt.Sprintf("unexpected %q", t.text)}
	}
	slices.Sort(p.vars)
	return &Expr{root: root, vars: slices.Compact(p.vars)}, nil
}

// --- lexer ------------------------------------------------------------------------------

type tokKind int

const (
	tEOF tokKind = iota
	tNum
	tIdent
	tOp
	tLParen
	tRParen
	tComma
)

type token struct {
	kind tokKind
	text string
	num  float64
	pos  int
}

var twoCharOps = []string{"<=", ">=", "==", "!=", "&&", "||"}

func lex(src string) ([]token, error) {
	var toks []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= '0' && c <= '9' || c == '.':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			if j < len(src) && (src[j] == 'e' || src[j] == 'E') {
				k := j + 1
				if k < len(src) && (src[k] == '+' || src[k] == '-') {
					k++
				}
				if k < len(src) && src[k] >= '0' && src[k] <= '9' {
					for j = k; j < len(src) && src[j] >= '0' && src[j] <= '9'; j++ {
					}
				}
			}
			v, err := strconv.ParseFloat(src[i:j], 64)
			if err != nil {
				return nil, &Error{i, fmt.Sprintf("invalid number %q", src[i:j])}
			}
			toks = append(toks, token{kind: tNum, text: src[i:j], num: v, pos: i})
			i = j
		case c == '_' || c < 128 && unicode.IsLetter(rune(c)):
			j := i
			for j < len(src) && (src[j] == '_' || src[j] < 128 && (unicode.IsLetter(rune(src[j])) || unicode.IsDigit(rune(src[j])))) {
				j++
			}
			toks = append(toks, token{kind: tIdent, text: strings.ToLower(src[i:j]), pos: i})
			i = j
		case c == '(':
			toks = append(toks, token{kind: tLParen, text: "(", pos: i})
			i++
		case c == ')':
			toks = append(toks, token{kind: tRParen, text: ")", pos: i})
			i++
		case c == ',' || c == ';': // ";" as in spreadsheets with a comma decimal separator
			toks = append(toks, token{kind: tComma, text: ",", pos: i})
			i++
		default:
			if i+1 < len(src) && slices.Contains(twoCharOps, src[i:i+2]) {
				toks = append(toks, token{kind: tOp, text: src[i : i+2], pos: i})
				i += 2
				continue
			}
			if strings.IndexByte("+-*/%^<>!", c) >= 0 {
				toks = append(toks, token{kind: tOp, text: string(c), pos: i})
				i++
				continue
			}
			return nil, &Error{i, fmt.Sprintf("unexpected character %q", src[i:i+1])}
		}
	}
	return append(toks, token{kind: tEOF, text: "end of formula", pos: len(src)}), nil
}

// --- parser -------------------------------------------------------------------------------

type parser struct {
	toks  []token
	i     int
	known []string
	vars  []string
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }

// Binary operators by precedence, lowest first.
var levels = [][]string{
	{"||"},
	{"&&"},
	{"==", "!=", "<", "<=", ">", ">="},
	{"+", "-"},
	{"*", "/", "%"},
}

func (p *parser) parseExpr(depth int) (node, error) { return p.parseLevel(0, depth) }

func (p *parser) parseLevel(level, depth int) (node, error) {
	if depth > maxDepth {
		return nil, &Error{p.peek().pos, "formula is nested too deeply"}
	}
	if level == len(levels) {
		return p.parseUnary(depth)
	}
	left, err := p.parseLevel(level+1, depth+1)
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tOp || !slices.Contains(levels[level], t.text) {
			return left, nil
		}
		p.next()
		right, err := p.parseLevel(level+1, depth+1)
		if err != nil {
			return nil, err
		}
		left = binary{op: t.text, l: left, r: right}
	}
}

func (p *parser) parseUnary(depth int) (node, error) {
	if depth > maxDepth {
		return nil, &Error{p.peek().pos, "formula is nested too deeply"}
	}
	if t := p.peek(); t.kind == tOp && (t.text == "-" || t.text == "+" || t.text == "!") {
		p.next()
		x, err := p.parseUnary(depth + 1)
		if err != nil {
			return nil, err
		}
		return unary{op: t.text, x: x}, nil
	}
	base, err := p.parsePrimary(depth)
	if err != nil {
		return nil, err
	}
	// "^" is right-associative and binds tighter than unary minus on its left: -2^2 = -4.
	if t := p.peek(); t.kind == tOp && t.text == "^" {
		p.next()
		exp, err := p.parseUnary(depth + 1)
		if err != nil {
			return nil, err
		}
		return binary{op: "^", l: base, r: exp}, nil
	}
	return base, nil
}

func (p *parser) parsePrimary(depth int) (node, error) {
	t := p.next()
	switch t.kind {
	case tNum:
		return num(t.num), nil
	case tLParen:
		x, err := p.parseExpr(depth + 1)
		if err != nil {
			return nil, err
		}
		if c := p.next(); c.kind != tRParen {
			return nil, &Error{c.pos, "missing \")\""}
		}
		return x, nil
	case tIdent:
		if p.peek().kind == tLParen {
			return p.parseCall(t, depth)
		}
		switch t.text {
		case "true":
			return num(1), nil
		case "false":
			return num(0), nil
		case "pi":
			return num(math.Pi), nil
		}
		if p.known != nil && !slices.Contains(p.known, t.text) {
			return nil, &Error{t.pos, fmt.Sprintf("unknown variable %q", t.text)}
		}
		p.vars = append(p.vars, t.text)
		return variable(t.text), nil
	case tEOF:
		return nil, &Error{t.pos, "formula ends unexpectedly"}
	default:
		return nil, &Error{t.pos, fmt.Sprintf("unexpected %q", t.text)}
	}
}

// functions maps names to implementations and allowed argument counts (max -1 = any).
var functions = map[string]struct {
	min, max int
	fn       func(args []float64) float64
}{
	"abs":   {1, 1, func(a []float64) float64 { return math.Abs(a[0]) }},
	"sqrt":  {1, 1, func(a []float64) float64 { return math.Sqrt(a[0]) }},
	"exp":   {1, 1, func(a []float64) float64 { return math.Exp(a[0]) }},
	"ln":    {1, 1, func(a []float64) float64 { return math.Log(a[0]) }},
	"log10": {1, 1, func(a []float64) float64 { return math.Log10(a[0]) }},
	"log": {1, 2, func(a []float64) float64 {
		if len(a) == 2 {
			return math.Log(a[0]) / math.Log(a[1])
		}
		return math.Log10(a[0])
	}},
	"floor": {1, 1, func(a []float64) float64 { return math.Floor(a[0]) }},
	"ceil":  {1, 1, func(a []float64) float64 { return math.Ceil(a[0]) }},
	"round": {1, 2, func(a []float64) float64 {
		if len(a) == 2 {
			p := math.Pow(10, math.Round(a[1]))
			return math.Round(a[0]*p) / p
		}
		return math.Round(a[0])
	}},
	"pow": {2, 2, func(a []float64) float64 { return math.Pow(a[0], a[1]) }},
	"min": {1, -1, func(a []float64) float64 { return slices.Min(a) }},
	"max": {1, -1, func(a []float64) float64 { return slices.Max(a) }},
}

func (p *parser) parseCall(name token, depth int) (node, error) {
	p.next() // (
	var args []node
	if p.peek().kind != tRParen {
		for {
			a, err := p.parseExpr(depth + 1)
			if err != nil {
				return nil, err
			}
			args = append(args, a)
			if p.peek().kind != tComma {
				break
			}
			p.next()
		}
	}
	if c := p.next(); c.kind != tRParen {
		return nil, &Error{c.pos, fmt.Sprintf("missing \")\" after the arguments of %s", name.text)}
	}
	if name.text == "if" {
		if len(args) != 3 {
			return nil, &Error{name.pos, "if needs 3 arguments: if(condition, then, else)"}
		}
		return cond{c: args[0], a: args[1], b: args[2]}, nil
	}
	f, ok := functions[name.text]
	if !ok {
		return nil, &Error{name.pos, fmt.Sprintf("unknown function %q", name.text)}
	}
	if len(args) < f.min || (f.max >= 0 && len(args) > f.max) {
		return nil, &Error{name.pos, fmt.Sprintf("wrong number of arguments for %s", name.text)}
	}
	return call{fn: f.fn, args: args}, nil
}

// --- evaluation ---------------------------------------------------------------------------

type node interface {
	eval(vars map[string]float64) float64
}

type num float64

func (n num) eval(map[string]float64) float64 { return float64(n) }

type variable string

func (v variable) eval(vars map[string]float64) float64 { return vars[string(v)] }

type unary struct {
	op string
	x  node
}

func (u unary) eval(vars map[string]float64) float64 {
	x := u.x.eval(vars)
	switch u.op {
	case "-":
		return -x
	case "!":
		return b2f(x == 0)
	}
	return x
}

type binary struct {
	op   string
	l, r node
}

func (b binary) eval(vars map[string]float64) float64 {
	l := b.l.eval(vars)
	switch b.op { // short-circuit logic
	case "&&":
		return b2f(l != 0 && b.r.eval(vars) != 0)
	case "||":
		return b2f(l != 0 || b.r.eval(vars) != 0)
	}
	r := b.r.eval(vars)
	switch b.op {
	case "+":
		return l + r
	case "-":
		return l - r
	case "*":
		return l * r
	case "/":
		return l / r
	case "%":
		return math.Mod(l, r)
	case "^":
		return math.Pow(l, r)
	case "<":
		return b2f(l < r)
	case "<=":
		return b2f(l <= r)
	case ">":
		return b2f(l > r)
	case ">=":
		return b2f(l >= r)
	case "==":
		return b2f(l == r)
	case "!=":
		return b2f(l != r)
	}
	return math.NaN()
}

type cond struct{ c, a, b node }

func (c cond) eval(vars map[string]float64) float64 {
	if c.c.eval(vars) != 0 {
		return c.a.eval(vars)
	}
	return c.b.eval(vars)
}

type call struct {
	fn   func([]float64) float64
	args []node
}

func (c call) eval(vars map[string]float64) float64 {
	vals := make([]float64, len(c.args))
	for i, a := range c.args {
		vals[i] = a.eval(vars)
	}
	return c.fn(vals)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
