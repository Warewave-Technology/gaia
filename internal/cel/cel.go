package cel

import (
	"fmt"
	"strings"

	gocel "cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
)

// costLimit bounds the work one evaluation may do; Gaia's expressions are
// small, so hitting it means a mistake, not a big input.
const costLimit = 100_000

// Program is a compiled expression.
type Program struct {
	src string
	prg gocel.Program
}

func (p *Program) String() string { return p.src }

// Naming compiles a Policy naming expression. It sees:
//
//	site      map: name
//	env, app  labels of the contract ("" when missing)
//	contract  the contract's name
//	labels    all labels of the contract
//	seq       the server's sequence number in its site
//
// and must return a string.
func Naming(src string) (*Program, error) {
	return compile(namingEnv, src, gocel.StringType)
}

// Rule compiles an AnsibleRole rule. It sees params (with defaults
// applied), size (cpu, memoryGiB, diskGiB), image (name, os), network
// (name, site) and labels, and must return a bool.
func Rule(src string) (*Program, error) {
	return compile(ruleEnv, src, gocel.BoolType)
}

// NamingVars builds the variables of a naming expression.
func NamingVars(contract string, labels map[string]string, site string, seq int) map[string]any {
	l := map[string]any{}
	for k, v := range labels {
		l[k] = v
	}
	return map[string]any{
		"site":     map[string]any{"name": site},
		"env":      labels["env"],
		"app":      labels["app"],
		"contract": contract,
		"labels":   l,
		"seq":      seq,
	}
}

// EvalString evaluates a program that returns a string.
func (p *Program) EvalString(vars map[string]any) (string, error) {
	v, err := p.eval(vars)
	if err != nil {
		return "", err
	}
	s, ok := v.Value().(string)
	if !ok {
		return "", fmt.Errorf("%s: result is %s, want string", p.src, v.Type().TypeName())
	}
	return s, nil
}

// EvalBool evaluates a program that returns a bool.
func (p *Program) EvalBool(vars map[string]any) (bool, error) {
	v, err := p.eval(vars)
	if err != nil {
		return false, err
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("%s: result is %s, want bool", p.src, v.Type().TypeName())
	}
	return b, nil
}

func (p *Program) eval(vars map[string]any) (ref.Val, error) {
	v, _, err := p.prg.Eval(vars)
	if err != nil {
		return nil, err
	}
	return v, nil
}

var (
	namingEnv = mustEnv(
		gocel.Variable("site", gocel.MapType(gocel.StringType, gocel.DynType)),
		gocel.Variable("env", gocel.StringType),
		gocel.Variable("app", gocel.StringType),
		gocel.Variable("contract", gocel.StringType),
		gocel.Variable("labels", gocel.MapType(gocel.StringType, gocel.StringType)),
		gocel.Variable("seq", gocel.IntType),
	)
	ruleEnv = mustEnv(
		gocel.Variable("params", gocel.MapType(gocel.StringType, gocel.DynType)),
		gocel.Variable("size", gocel.MapType(gocel.StringType, gocel.DynType)),
		gocel.Variable("image", gocel.MapType(gocel.StringType, gocel.DynType)),
		gocel.Variable("network", gocel.MapType(gocel.StringType, gocel.DynType)),
		gocel.Variable("labels", gocel.MapType(gocel.StringType, gocel.StringType)),
	)
)

// common is what every Gaia expression may use: the strings extension
// (substring, lowerAscii, replace, format...), comparisons across numeric
// types and pad.
var common = []gocel.EnvOption{
	ext.Strings(),
	gocel.CrossTypeNumericComparisons(true),
	gocel.Function("pad",
		gocel.Overload("pad_int_int", []*gocel.Type{gocel.IntType, gocel.IntType}, gocel.StringType,
			gocel.BinaryBinding(pad))),
}

func mustEnv(opts ...gocel.EnvOption) *gocel.Env {
	env, err := gocel.NewEnv(append(append([]gocel.EnvOption{}, common...), opts...)...)
	if err != nil {
		panic(err)
	}
	return env
}

// pad(n, width) writes n in decimal, left-padded with zeros to width.
func pad(n, width ref.Val) ref.Val {
	i, ok1 := n.(types.Int)
	w, ok2 := width.(types.Int)
	if !ok1 || !ok2 {
		return types.NewErr("pad: want (int, int)")
	}
	if i < 0 || w < 0 || w > 20 {
		return types.NewErr("pad(%d, %d): want n >= 0 and 0 <= width <= 20", i, w)
	}
	return types.String(fmt.Sprintf("%0*d", int(w), int64(i)))
}

func compile(env *gocel.Env, src string, want *gocel.Type) (*Program, error) {
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("empty expression")
	}
	ast, iss := env.Compile(src)
	if iss.Err() != nil {
		// One line per problem: column and message, without the caret
		// drawing, so that errors stay one per line.
		var msgs []string
		for _, e := range iss.Errors() {
			msgs = append(msgs, fmt.Sprintf("column %d: %s", e.Location.Column()+1, e.Message))
		}
		return nil, fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	if got := ast.OutputType(); !got.IsExactType(want) && !got.IsExactType(gocel.DynType) {
		return nil, fmt.Errorf("returns %s, want %s", got, want)
	}
	prg, err := env.Program(ast, gocel.CostLimit(costLimit))
	if err != nil {
		return nil, err
	}
	return &Program{src: src, prg: prg}, nil
}
