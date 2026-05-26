package calculate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(CalculateTool)) }

// CalculateTool evaluates simple arithmetic expressions.
type CalculateTool struct{}

func (t *CalculateTool) Name() string { return "calculate" }
func (t *CalculateTool) Desc() string {
	return "Evaluate an arithmetic expression. Supports +, -, *, / and parentheses. Example: \"0.088 + (0.015 * 0.5)\""
}
func (t *CalculateTool) Caps() []Capability { return nil } // pure transform — no side effects

func (t *CalculateTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"expression": {Type: "string", Description: "The arithmetic expression to evaluate (e.g. \"0.088 + 0.015 * 0.5\")"},
	}
}

func (t *CalculateTool) Run(args map[string]any) (string, error) {
	expr, _ := args["expression"].(string)
	if expr == "" {
		return "", fmt.Errorf("expression is required")
	}

	result, err := Eval(expr)
	if err != nil {
		return "", fmt.Errorf("failed to evaluate %q: %w", expr, err)
	}

	// Format to avoid floating point noise but preserve precision.
	formatted := strconv.FormatFloat(result, 'f', -1, 64)
	// Trim excessive trailing zeros but keep at least one decimal place for clarity.
	if strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(formatted, "0")
		formatted = strings.TrimRight(formatted, ".")
	}
	return fmt.Sprintf("%s = %s", expr, formatted), nil
}

// Eval parses and evaluates an arithmetic expression using Go's AST parser.
func Eval(expr string) (float64, error) {
	node, err := parser.ParseExpr(expr)
	if err != nil {
		return 0, fmt.Errorf("parse error: %w", err)
	}
	return evalNode(node)
}

func evalNode(node ast.Expr) (float64, error) {
	switch n := node.(type) {
	case *ast.BasicLit:
		if n.Kind != token.INT && n.Kind != token.FLOAT {
			return 0, fmt.Errorf("unsupported literal: %s", n.Value)
		}
		return strconv.ParseFloat(n.Value, 64)

	case *ast.UnaryExpr:
		val, err := evalNode(n.X)
		if err != nil {
			return 0, err
		}
		switch n.Op {
		case token.SUB:
			return -val, nil
		case token.ADD:
			return val, nil
		default:
			return 0, fmt.Errorf("unsupported unary operator: %s", n.Op)
		}

	case *ast.BinaryExpr:
		left, err := evalNode(n.X)
		if err != nil {
			return 0, err
		}
		right, err := evalNode(n.Y)
		if err != nil {
			return 0, err
		}
		switch n.Op {
		case token.ADD:
			return left + right, nil
		case token.SUB:
			return left - right, nil
		case token.MUL:
			return left * right, nil
		case token.QUO:
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			return left / right, nil
		default:
			return 0, fmt.Errorf("unsupported operator: %s", n.Op)
		}

	case *ast.ParenExpr:
		return evalNode(n.X)

	case *ast.CallExpr:
		// Support basic math functions.
		fn, ok := n.Fun.(*ast.Ident)
		if !ok || len(n.Args) != 1 {
			return 0, fmt.Errorf("unsupported function call")
		}
		arg, err := evalNode(n.Args[0])
		if err != nil {
			return 0, err
		}
		switch fn.Name {
		case "abs":
			return math.Abs(arg), nil
		case "sqrt":
			return math.Sqrt(arg), nil
		case "round":
			return math.Round(arg), nil
		default:
			return 0, fmt.Errorf("unsupported function: %s", fn.Name)
		}

	default:
		return 0, fmt.Errorf("unsupported expression type: %T", node)
	}
}
