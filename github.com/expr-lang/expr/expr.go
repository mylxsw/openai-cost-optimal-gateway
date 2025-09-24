package expr

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/expr-lang/expr/vm"
)

type Option func(*config)

type config struct{}

func Env(_ interface{}) Option {
	return func(c *config) {}
}

func Compile(expression string, opts ...Option) (*vm.Program, error) {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}
	tokens, err := tokenize(expression)
	if err != nil {
		return nil, err
	}
	parser := &parser{tokens: tokens}
	node, err := parser.parseExpression()
	if err != nil {
		return nil, err
	}
	program := &vm.Program{
		Eval: func(vars map[string]interface{}) (interface{}, error) {
			return node.eval(vars)
		},
	}
	return program, nil
}

type tokenType int

const (
	tokenEOF tokenType = iota
	tokenIdentifier
	tokenNumber
	tokenString
	tokenAnd
	tokenOr
	tokenGreater
	tokenGreaterEqual
	tokenLess
	tokenLessEqual
	tokenEqual
	tokenNotEqual
	tokenLParen
	tokenRParen
)

type token struct {
	typ     tokenType
	literal string
}

func tokenize(input string) ([]token, error) {
	tokens := []token{}
	runes := []rune(input)
	for i := 0; i < len(runes); {
		ch := runes[i]
		if unicode.IsSpace(ch) {
			i++
			continue
		}
		switch ch {
		case '(':
			tokens = append(tokens, token{typ: tokenLParen})
			i++
			continue
		case ')':
			tokens = append(tokens, token{typ: tokenRParen})
			i++
			continue
		case '&':
			if i+1 < len(runes) && runes[i+1] == '&' {
				tokens = append(tokens, token{typ: tokenAnd})
				i += 2
				continue
			}
			return nil, fmt.Errorf("unexpected character '&'")
		case '|':
			if i+1 < len(runes) && runes[i+1] == '|' {
				tokens = append(tokens, token{typ: tokenOr})
				i += 2
				continue
			}
			return nil, fmt.Errorf("unexpected character '|")
		case '>':
			if i+1 < len(runes) && runes[i+1] == '=' {
				tokens = append(tokens, token{typ: tokenGreaterEqual})
				i += 2
			} else {
				tokens = append(tokens, token{typ: tokenGreater})
				i++
			}
			continue
		case '<':
			if i+1 < len(runes) && runes[i+1] == '=' {
				tokens = append(tokens, token{typ: tokenLessEqual})
				i += 2
			} else {
				tokens = append(tokens, token{typ: tokenLess})
				i++
			}
			continue
		case '=':
			if i+1 < len(runes) && runes[i+1] == '=' {
				tokens = append(tokens, token{typ: tokenEqual})
				i += 2
				continue
			}
			return nil, fmt.Errorf("unexpected character '='")
		case '!':
			if i+1 < len(runes) && runes[i+1] == '=' {
				tokens = append(tokens, token{typ: tokenNotEqual})
				i += 2
				continue
			}
			return nil, fmt.Errorf("unexpected character '!'")
		case '\'':
			start := i + 1
			i++
			for i < len(runes) && runes[i] != '\'' {
				i++
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("unterminated string literal")
			}
			literal := string(runes[start:i])
			tokens = append(tokens, token{typ: tokenString, literal: literal})
			i++
			continue
		case '"':
			start := i + 1
			i++
			for i < len(runes) && runes[i] != '"' {
				i++
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("unterminated string literal")
			}
			literal := string(runes[start:i])
			tokens = append(tokens, token{typ: tokenString, literal: literal})
			i++
			continue
		}

		if unicode.IsLetter(ch) || ch == '_' {
			start := i
			i++
			for i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_' || runes[i] == '-') {
				i++
			}
			tokens = append(tokens, token{typ: tokenIdentifier, literal: string(runes[start:i])})
			continue
		}
		if unicode.IsDigit(ch) {
			start := i
			i++
			for i < len(runes) && (unicode.IsDigit(runes[i]) || runes[i] == '.') {
				i++
			}
			tokens = append(tokens, token{typ: tokenNumber, literal: string(runes[start:i])})
			continue
		}
		return nil, fmt.Errorf("unexpected character '%c'", ch)
	}
	tokens = append(tokens, token{typ: tokenEOF})
	return tokens, nil
}

type node interface {
	eval(map[string]interface{}) (interface{}, error)
}

type parser struct {
	tokens []token
	pos    int
}

func (p *parser) parseExpression() (node, error) {
	return p.parseOr()
}

func (p *parser) parseOr() (node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.match(tokenOr) {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &binaryNode{left: left, right: right, op: "||"}
	}
	return left, nil
}

func (p *parser) parseAnd() (node, error) {
	left, err := p.parseComparison()
	if err != nil {
		return nil, err
	}
	for p.match(tokenAnd) {
		right, err := p.parseComparison()
		if err != nil {
			return nil, err
		}
		left = &binaryNode{left: left, right: right, op: "&&"}
	}
	return left, nil
}

func (p *parser) parseComparison() (node, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek().typ {
		case tokenEqual:
			p.advance()
			right, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			left = &binaryNode{left: left, right: right, op: "=="}
		case tokenNotEqual:
			p.advance()
			right, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			left = &binaryNode{left: left, right: right, op: "!="}
		case tokenGreater:
			p.advance()
			right, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			left = &binaryNode{left: left, right: right, op: ">"}
		case tokenGreaterEqual:
			p.advance()
			right, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			left = &binaryNode{left: left, right: right, op: ">="}
		case tokenLess:
			p.advance()
			right, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			left = &binaryNode{left: left, right: right, op: "<"}
		case tokenLessEqual:
			p.advance()
			right, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			left = &binaryNode{left: left, right: right, op: "<="}
		default:
			return left, nil
		}
	}
}

func (p *parser) parsePrimary() (node, error) {
	tok := p.peek()
	switch tok.typ {
	case tokenIdentifier:
		p.advance()
		lower := strings.ToLower(tok.literal)
		if lower == "true" {
			return &literalNode{value: true}, nil
		}
		if lower == "false" {
			return &literalNode{value: false}, nil
		}
		return &identifierNode{name: tok.literal}, nil
	case tokenNumber:
		p.advance()
		if strings.Contains(tok.literal, ".") {
			val, err := strconv.ParseFloat(tok.literal, 64)
			if err != nil {
				return nil, err
			}
			return &literalNode{value: val}, nil
		}
		val, err := strconv.ParseInt(tok.literal, 10, 64)
		if err != nil {
			return nil, err
		}
		return &literalNode{value: val}, nil
	case tokenString:
		p.advance()
		return &literalNode{value: tok.literal}, nil
	case tokenLParen:
		p.advance()
		expr, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if !p.match(tokenRParen) {
			return nil, fmt.Errorf("missing closing parenthesis")
		}
		return expr, nil
	default:
		return nil, fmt.Errorf("unexpected token %v", tok.typ)
	}
}

func (p *parser) peek() token {
	if p.pos >= len(p.tokens) {
		return token{typ: tokenEOF}
	}
	return p.tokens[p.pos]
}

func (p *parser) advance() token {
	tok := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return tok
}

func (p *parser) match(tt tokenType) bool {
	if p.peek().typ == tt {
		p.advance()
		return true
	}
	return false
}

type binaryNode struct {
	left  node
	right node
	op    string
}

type identifierNode struct {
	name string
}

type literalNode struct {
	value interface{}
}

func (n *binaryNode) eval(vars map[string]interface{}) (interface{}, error) {
	leftVal, err := n.left.eval(vars)
	if err != nil {
		return nil, err
	}
	rightVal, err := n.right.eval(vars)
	if err != nil {
		return nil, err
	}
	switch n.op {
	case "&&":
		return toBool(leftVal) && toBool(rightVal), nil
	case "||":
		return toBool(leftVal) || toBool(rightVal), nil
	case "==":
		return equal(leftVal, rightVal), nil
	case "!=":
		return !equal(leftVal, rightVal), nil
	case ">":
		return compareNumbers(leftVal, rightVal, func(a, b float64) bool { return a > b })
	case ">=":
		return compareNumbers(leftVal, rightVal, func(a, b float64) bool { return a >= b })
	case "<":
		return compareNumbers(leftVal, rightVal, func(a, b float64) bool { return a < b })
	case "<=":
		return compareNumbers(leftVal, rightVal, func(a, b float64) bool { return a <= b })
	default:
		return nil, fmt.Errorf("unknown operator %s", n.op)
	}
}

func (n *identifierNode) eval(vars map[string]interface{}) (interface{}, error) {
	if vars == nil {
		return nil, fmt.Errorf("missing variables")
	}
	val, ok := vars[n.name]
	if !ok {
		return nil, fmt.Errorf("unknown identifier %s", n.name)
	}
	return val, nil
}

func (n *literalNode) eval(vars map[string]interface{}) (interface{}, error) {
	return n.value, nil
}

func toBool(v interface{}) bool {
	switch val := v.(type) {
	case bool:
		return val
	case int:
		return val != 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	case string:
		lower := strings.ToLower(val)
		return lower == "true" || lower == "1"
	default:
		return false
	}
}

func equal(a, b interface{}) bool {
	switch left := a.(type) {
	case string:
		right, ok := b.(string)
		return ok && left == right
	case int:
		return compareNumeric(float64(left), b)
	case int64:
		return compareNumeric(float64(left), b)
	case float64:
		return compareNumeric(left, b)
	case bool:
		rb, ok := b.(bool)
		return ok && left == rb
	default:
		return false
	}
}

func compareNumeric(left float64, right interface{}) bool {
	switch r := right.(type) {
	case int:
		return left == float64(r)
	case int64:
		return left == float64(r)
	case float64:
		return left == r
	default:
		return false
	}
}

func compareNumbers(left, right interface{}, cmp func(a, b float64) bool) (bool, error) {
	lf, lok := toFloat(left)
	rf, rok := toFloat(right)
	if !lok || !rok {
		return false, fmt.Errorf("operands must be numeric")
	}
	return cmp(lf, rf), nil
}

func toFloat(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case float64:
		return val, true
	case string:
		parsed, err := strconv.ParseFloat(val, 64)
		if err == nil {
			return parsed, true
		}
		return 0, false
	default:
		return 0, false
	}
}
