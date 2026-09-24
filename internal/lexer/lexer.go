// Package lexer turns Pygo source text into tokens.
//
// Statements are terminated by newlines (automatic terminator insertion, as
// in Go) or by ';'. Newlines inside (...) and [...] are ignored, and a line
// starting with ".method" continues the previous line, so long call chains
// can be split over several lines.
package lexer

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/marcodc74/pygo/internal/diag"
)

type Kind int

const (
	EOF Kind = iota
	NEWLINE
	IDENT
	INT
	FLOAT
	STRING
	KEYWORD
	OP
)

func (k Kind) String() string {
	return [...]string{"end of file", "newline", "identifier", "integer", "float", "string", "keyword", "operator"}[k]
}

var Keywords = map[string]bool{
	"fn": true, "let": true, "var": true, "if": true, "else": true, "for": true,
	"in": true, "while": true, "break": true, "continue": true, "return": true,
	"match": true, "struct": true, "enum": true, "impl": true, "import": true,
	"as": true, "test": true, "assert": true, "fail": true, "try": true,
	"catch": true, "spawn": true, "defer": true, "uses": true, "requires": true,
	"ensures": true, "true": true, "false": true, "nil": true, "and": true,
	"or": true, "not": true,
}

// StrPart is a piece of a string literal: either literal text or an
// interpolated expression "${expr}" / "${expr:spec}".
type StrPart struct {
	IsExpr bool
	Text   string
	Format string
	Pos    diag.Pos
}

type Token struct {
	Kind   Kind
	Text   string
	Pos    diag.Pos
	Parts  []StrPart // STRING only
	Raw    bool      // STRING only: r"..." literal
	Doc    string    // "///" doc comment lines immediately preceding the token
	Off    int       // rune offset of the token start
	End    int       // rune offset just after the token
	DocOff int       // rune offset of the first doc line (-1 if none)
}

func (t Token) String() string {
	switch t.Kind {
	case EOF, NEWLINE:
		return t.Kind.String()
	case STRING:
		return "string literal"
	}
	return fmt.Sprintf("'%s'", t.Text)
}

type Lexer struct {
	src    []rune
	i      int
	line   int
	col    int
	stack  []rune
	toks   []Token
	doc    []string
	docOff int
	start  int
	Diags  diag.List
	offset diag.Pos // for sub-lexers of interpolated expressions
}

func New(file, src string) *Lexer {
	l := &Lexer{src: []rune(src), line: 1, col: 1}
	l.Diags.File = file
	return l
}

// NewAt creates a lexer whose positions start at pos (used for the
// expressions embedded in string interpolations).
func NewAt(file, src string, pos diag.Pos) *Lexer {
	l := New(file, src)
	l.line, l.col = pos.Line, pos.Col
	return l
}

func (l *Lexer) peek(n int) rune {
	if l.i+n < len(l.src) {
		return l.src[l.i+n]
	}
	return 0
}

func (l *Lexer) advance() rune {
	r := l.src[l.i]
	l.i++
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

func (l *Lexer) pos() diag.Pos { return diag.Pos{Line: l.line, Col: l.col} }

func (l *Lexer) emit(t Token) {
	t.DocOff = -1
	t.Off = l.start
	t.End = l.i
	if len(l.doc) > 0 && t.Kind != NEWLINE {
		t.Doc = strings.Join(l.doc, "\n")
		t.DocOff = l.docOff
		l.doc = nil
	}
	l.toks = append(l.toks, t)
}

func (l *Lexer) inGroup() bool {
	return len(l.stack) > 0 && l.stack[len(l.stack)-1] != '{'
}

// needsTerminator reports whether a newline after the last token ends a statement.
func (l *Lexer) needsTerminator() bool {
	if len(l.toks) == 0 || l.inGroup() {
		return false
	}
	t := l.toks[len(l.toks)-1]
	switch t.Kind {
	case IDENT, INT, FLOAT, STRING:
		return true
	case KEYWORD:
		switch t.Text {
		case "true", "false", "nil", "return", "break", "continue":
			return true
		}
	case OP:
		switch t.Text {
		case ")", "]", "}", "?", "!":
			return true
		}
	}
	return false
}

// continuesWithDot reports whether the next non-blank line starts with ".x"
// (method chaining across lines).
func (l *Lexer) continuesWithDot() bool {
	j := l.i
	for j < len(l.src) {
		c := l.src[j]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			j++
			continue
		}
		if c == '/' && j+1 < len(l.src) && l.src[j+1] == '/' {
			for j < len(l.src) && l.src[j] != '\n' {
				j++
			}
			continue
		}
		break
	}
	return j+1 < len(l.src) && l.src[j] == '.' && l.src[j+1] != '.'
}

func (l *Lexer) Tokenize() []Token {
	for l.i < len(l.src) {
		c := l.peek(0)
		l.start = l.i
		switch {
		case c == '\n':
			p := l.pos()
			l.advance()
			if l.needsTerminator() && !l.continuesWithDot() {
				l.toks = append(l.toks, Token{Kind: NEWLINE, Text: "\n", Pos: p})
			}
		case c == ' ' || c == '\t' || c == '\r':
			l.advance()
		case c == '/' && l.peek(1) == '/':
			l.comment()
		case c == '"' || (c == 'r' && l.peek(1) == '"'):
			l.str()
		case isLetter(c):
			l.ident()
		case unicode.IsDigit(c):
			l.number()
		default:
			l.op()
		}
	}
	if l.needsTerminator() {
		l.toks = append(l.toks, Token{Kind: NEWLINE, Text: "\n", Pos: l.pos()})
	}
	for len(l.stack) > 0 {
		open := l.stack[len(l.stack)-1]
		l.stack = l.stack[:len(l.stack)-1]
		l.Diags.Errorf("E0103", l.pos(), fmt.Sprintf("add the closing '%c'", closing(open)), "unclosed '%c'", open)
	}
	l.toks = append(l.toks, Token{Kind: EOF, Pos: l.pos()})
	return l.toks
}

func closing(r rune) rune {
	switch r {
	case '(':
		return ')'
	case '[':
		return ']'
	}
	return '}'
}

func isLetter(c rune) bool { return c == '_' || unicode.IsLetter(c) }

func (l *Lexer) comment() {
	isDoc := l.peek(2) == '/' && l.peek(3) != '/'
	start := l.i
	for l.i < len(l.src) && l.peek(0) != '\n' {
		l.advance()
	}
	if isDoc {
		if len(l.doc) == 0 {
			l.docOff = start
		}
		text := string(l.src[start+3 : l.i])
		l.doc = append(l.doc, strings.TrimPrefix(text, " "))
	}
}

func (l *Lexer) ident() {
	p := l.pos()
	start := l.i
	for l.i < len(l.src) && (isLetter(l.peek(0)) || unicode.IsDigit(l.peek(0))) {
		l.advance()
	}
	text := string(l.src[start:l.i])
	kind := IDENT
	if Keywords[text] {
		kind = KEYWORD
	}
	l.emit(Token{Kind: kind, Text: text, Pos: p})
}

func (l *Lexer) number() {
	p := l.pos()
	start := l.i
	kind := INT
	if l.peek(0) == '0' && strings.ContainsRune("xXbBoO", l.peek(1)) {
		l.advance()
		l.advance()
		for l.i < len(l.src) && (isHex(l.peek(0)) || l.peek(0) == '_') {
			l.advance()
		}
	} else {
		l.digits()
		if l.peek(0) == '.' && unicode.IsDigit(l.peek(1)) {
			kind = FLOAT
			l.advance()
			l.digits()
		}
		if l.peek(0) == 'e' || l.peek(0) == 'E' {
			n := 1
			if l.peek(1) == '+' || l.peek(1) == '-' {
				n = 2
			}
			if unicode.IsDigit(l.peek(n)) {
				kind = FLOAT
				for k := 0; k < n; k++ {
					l.advance()
				}
				l.digits()
			}
		}
	}
	if l.i < len(l.src) && isLetter(l.peek(0)) {
		l.Diags.Errorf("E0102", l.pos(), "separate the number from the name with a space or an operator", "invalid character %q in number literal", l.peek(0))
		for l.i < len(l.src) && (isLetter(l.peek(0)) || unicode.IsDigit(l.peek(0))) {
			l.advance()
		}
	}
	l.emit(Token{Kind: kind, Text: string(l.src[start:l.i]), Pos: p})
}

func isHex(c rune) bool {
	return unicode.IsDigit(c) || strings.ContainsRune("abcdefABCDEF", c)
}

func (l *Lexer) digits() {
	for l.i < len(l.src) && (unicode.IsDigit(l.peek(0)) || l.peek(0) == '_') {
		l.advance()
	}
}

var ops3 = []string{"..="}
var ops2 = []string{"==", "!=", "<=", ">=", "+=", "-=", "*=", "/=", "%=", "->", "=>", "..", "??"}

func (l *Lexer) op() {
	p := l.pos()
	rest := string(l.src[l.i:min(l.i+3, len(l.src))])
	for _, group := range [][]string{ops3, ops2} {
		for _, o := range group {
			if strings.HasPrefix(rest, o) {
				for range o {
					l.advance()
				}
				l.emit(Token{Kind: OP, Text: o, Pos: p})
				return
			}
		}
	}
	c := l.advance()
	switch c {
	case '(', '[', '{':
		l.stack = append(l.stack, c)
	case ')', ']', '}':
		if len(l.stack) == 0 || closing(l.stack[len(l.stack)-1]) != c {
			l.Diags.Errorf("E0103", p, "check that brackets are balanced", "unexpected '%c'", c)
		} else {
			l.stack = l.stack[:len(l.stack)-1]
		}
	case ';':
		l.Diags.Errorf("E0107", p, "end statements with a newline", "';' is not used in Pygo")
		return
	case '+', '-', '*', '/', '%', '=', '<', '>', '.', ',', ':', '?', '!', '|':
	default:
		l.Diags.Errorf("E0101", p, "", "unexpected character %q", c)
		return
	}
	l.emit(Token{Kind: OP, Text: string(c), Pos: p})
}

// str lexes "...", """...""", r"..." and r"""...""".
func (l *Lexer) str() {
	p := l.pos()
	raw := false
	if l.peek(0) == 'r' {
		raw = true
		l.advance()
	}
	triple := l.peek(0) == '"' && l.peek(1) == '"' && l.peek(2) == '"'
	if triple {
		l.advance()
		l.advance()
		l.advance()
		// a newline right after the opening quotes is not part of the string
		if l.peek(0) == '\n' {
			l.advance()
		} else if l.peek(0) == '\r' && l.peek(1) == '\n' {
			l.advance()
			l.advance()
		}
	} else {
		l.advance()
	}
	var parts []StrPart
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 || len(parts) == 0 {
			parts = append(parts, StrPart{Text: buf.String()})
			buf.Reset()
		}
	}
	for {
		if l.i >= len(l.src) {
			l.Diags.Errorf("E0104", p, "add the closing quote", "unterminated string literal")
			break
		}
		c := l.peek(0)
		if triple {
			if c == '"' && l.peek(1) == '"' && l.peek(2) == '"' {
				l.advance()
				l.advance()
				l.advance()
				break
			}
		} else {
			if c == '"' {
				l.advance()
				break
			}
			if c == '\n' {
				l.Diags.Errorf("E0104", p, "use \"\"\"...\"\"\" for multi-line strings or \\n for a newline", "unterminated string literal")
				break
			}
		}
		if raw {
			buf.WriteRune(l.advance())
			continue
		}
		if c == '\\' {
			ep := l.pos()
			l.advance()
			if l.i >= len(l.src) {
				continue
			}
			e := l.advance()
			switch e {
			case 'n':
				buf.WriteRune('\n')
			case 't':
				buf.WriteRune('\t')
			case 'r':
				buf.WriteRune('\r')
			case '0':
				buf.WriteRune(0)
			case '\\', '"', '{', '}', '\'', '$':
				buf.WriteRune(e)
			case 'u':
				l.unicodeEscape(&buf, ep)
			default:
				l.Diags.Errorf("E0105", ep, `valid escapes: \n \t \r \0 \\ \" \$ \u{XXXX}`, "invalid escape sequence '\\%c'", e)
			}
			continue
		}
		if c == '$' && l.peek(1) == '{' {
			flush()
			l.advance()
			parts = append(parts, l.interpolation())
			continue
		}
		buf.WriteRune(l.advance())
	}
	if buf.Len() > 0 || len(parts) == 0 {
		parts = append(parts, StrPart{Text: buf.String()})
	}
	l.emit(Token{Kind: STRING, Text: "string", Pos: p, Parts: parts, Raw: raw})
}

func (l *Lexer) unicodeEscape(buf *strings.Builder, ep diag.Pos) {
	if l.peek(0) != '{' {
		l.Diags.Errorf("E0105", ep, `write \u{1F600}`, "invalid unicode escape")
		return
	}
	l.advance()
	var hex strings.Builder
	for l.i < len(l.src) && l.peek(0) != '}' && l.peek(0) != '"' {
		hex.WriteRune(l.advance())
	}
	if l.peek(0) == '}' {
		l.advance()
	}
	var v rune
	if _, err := fmt.Sscanf(hex.String(), "%x", &v); err != nil {
		l.Diags.Errorf("E0105", ep, `write \u{1F600}`, "invalid unicode escape")
		return
	}
	buf.WriteRune(v)
}

// interpolation lexes "${expr}" or "${expr:spec}" inside a string
// (the '$' has already been consumed).
func (l *Lexer) interpolation() StrPart {
	start := l.pos()
	l.advance() // {
	exprPos := l.pos()
	depth := 0
	var text strings.Builder
	format := ""
	inStr := false
	for l.i < len(l.src) {
		c := l.peek(0)
		if c == '\n' && !inStr {
			break
		}
		if inStr {
			if c == '\\' {
				text.WriteRune(l.advance())
				if l.i < len(l.src) {
					text.WriteRune(l.advance())
				}
				continue
			}
			if c == '"' {
				inStr = false
			}
			text.WriteRune(l.advance())
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '(', '[', '{':
			depth++
		case ')', ']':
			depth--
		case '}':
			if depth == 0 {
				l.advance()
				expr := strings.TrimSpace(text.String())
				if expr == "" {
					l.Diags.Errorf("E0106", start, `write \${ for a literal '${'`, "empty interpolation '${}' in string")
				}
				return StrPart{IsExpr: true, Text: text.String(), Format: format, Pos: exprPos}
			}
			depth--
		case ':':
			if depth == 0 {
				l.advance()
				var f strings.Builder
				for l.i < len(l.src) && l.peek(0) != '}' && l.peek(0) != '\n' && l.peek(0) != '"' {
					f.WriteRune(l.advance())
				}
				format = f.String()
				continue
			}
		}
		text.WriteRune(l.advance())
	}
	l.Diags.Errorf("E0106", start, `close the interpolation with '}' or write \${ for a literal '${'`, "unterminated interpolation in string")
	return StrPart{IsExpr: true, Text: text.String(), Pos: exprPos}
}
