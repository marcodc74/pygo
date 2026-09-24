package diag

// Explanation documents a diagnostic or runtime panic code.
type Explanation struct {
	Code   string `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Wrong  string `json:"wrong,omitempty"`
	Right  string `json:"right,omitempty"`
}

// Explanations lists every stable code (compile-time E/W, runtime R).
var Explanations = map[string]Explanation{
	"E0101": {Title: "unexpected character", Detail: "The source contains a character that is not part of Pygo syntax."},
	"E0102": {Title: "invalid number", Detail: "A number literal is directly followed by letters.", Wrong: "let x = 10px", Right: "let x = 10"},
	"E0103": {Title: "unbalanced brackets", Detail: "A (, [ or { is not closed, or a closing bracket has no opener."},
	"E0104": {Title: "unterminated string", Detail: `A "..." string must end on the same line. Use """...""" for multi-line text.`},
	"E0105": {Title: "invalid escape", Detail: `Valid escapes: \n \t \r \0 \\ \" \$ \u{XXXX}. Use r"..." for raw text.`},
	"E0106": {Title: "bad interpolation", Detail: `Interpolation is ${expr} or ${expr:spec}. Write \${ for a literal "${".`},
	"E0107": {Title: "semicolon", Detail: "Pygo has no ';'. End each statement with a newline."},
	"E0110": {Title: "syntax error", Detail: "The parser expected a different token. The message names what was expected."},
	"E0111": {Title: "expected expression", Detail: "An expression is missing. An operator at the end of a line continues it on the next line."},
	"E0112": {Title: "statement terminator", Detail: "Only one statement per line."},
	"E0113": {Title: "missing parameter type", Detail: "Parameters of declared functions need a type. Only lambda parameters may omit it.", Wrong: "fn f(x) {}", Right: "fn f(x: Int) {}"},
	"E0114": {Title: "call required", Detail: "defer and spawn take a function call.", Wrong: "spawn worker", Right: "spawn worker(ch)"},
	"E0115": {Title: "invalid assignment target", Detail: "Only variables, indexes a[i] and fields x.f can be assigned."},
	"E0116": {Title: "ambiguous operators", Detail: "Mixing and/or, or ?? with other operators, needs parentheses. Comparisons cannot be chained.", Wrong: "a and b or c", Right: "(a and b) or c"},
	"E0117": {Title: "invalid literal", Detail: "The number does not fit its type (Int is 64-bit signed)."},
	"E0118": {Title: "else placement", Detail: "'else' must be on the same line as the closing '}'.", Wrong: "}\nelse {", Right: "} else {"},
	"E0119": {Title: "invalid pattern", Detail: "Patterns are: _, name, literal, lo..=hi, Enum.Variant(p, ...), alternatives with |."},

	"E0201": {Title: "undefined name", Detail: "The name is not declared. The hint suggests the closest visible name."},
	"E0202": {Title: "immutable", Detail: "let bindings, parameters and module-level names cannot be reassigned.", Wrong: "let n = 0\nn += 1", Right: "var n = 0\nn += 1"},
	"E0203": {Title: "unknown type", Detail: "The type name is not declared, not imported, or a generic parameter is not declared in fn name[T]."},
	"E0204": {Title: "unknown field or method", Detail: "The value's type has no such member. The hint lists the closest ones."},
	"E0205": {Title: "unknown module member", Detail: "The module does not export this name. See `pygo describe <module>`."},
	"E0206": {Title: "shadowing", Detail: "Names are unique within a function and may not hide module-level or builtin names. Pick a new name.", Wrong: "let x = 1\nif c { let x = 2 }", Right: "let x = 1\nif c { let x2 = 2 }"},
	"E0207": {Title: "duplicate declaration", Detail: "The same name is declared twice in one scope."},
	"E0208": {Title: "module not imported", Detail: "Standard modules must be imported explicitly.", Wrong: "json.encode(x)", Right: "import \"json\"\n...\njson.encode(x)"},
	"E0209": {Title: "global mutable state", Detail: "Only 'let' constants are allowed at module level. Keep state in local variables or pass it as parameters."},
	"W0201": {Title: "unused variable", Detail: "The variable is never read. Remove it or prefix it with _."},
	"W0202": {Title: "unused import", Detail: "The module is imported but never used."},

	"E0301": {Title: "type mismatch", Detail: "A value of one type is used where another is required. There are no implicit conversions."},
	"E0302": {Title: "invalid operands", Detail: "The operator is not defined for these types. Int and Float never mix: use float(n) or int(x). Build strings with interpolation."},
	"E0303": {Title: "condition not Bool", Detail: "Conditions must be Bool; there is no truthiness.", Wrong: "if items { ... }", Right: "if not items.is_empty() { ... }"},
	"E0304": {Title: "not callable", Detail: "The expression is not a function. Structs are built with literals: User{name: \"a\"}."},
	"E0305": {Title: "wrong arguments", Detail: "Too many/too few arguments, an unknown parameter name, or an argument given twice. The hint shows the signature."},
	"E0306": {Title: "argument must be named", Detail: "Only the first argument may be positional; the others must be named. This prevents swapped arguments.", Wrong: "transfer(10, \"alice\", \"bob\")", Right: "transfer(10, from: \"alice\", to: \"bob\")"},
	"E0307": {Title: "missing return", Detail: "A function with a result type must end every path with return, fail or panic."},
	"E0308": {Title: "invalid return", Detail: "return with a value in a function without result type, or without a value in one that has it."},
	"E0310": {Title: "possibly nil", Detail: "The value has an optional type T? and may be nil. Check it first (if x != nil { ... }, if x == nil { return }) or use x ?? default.", Wrong: "let v = m.get(k)\nprint(v.len())", Right: "let v = m.get(k) ?? \"\"\nprint(v.len())"},
	"W0302": {Title: "useless ??", Detail: "The left side of ?? can never be nil."},

	"E0401": {Title: "unhandled failure", Detail: "The call can fail (its type is -> !T). Propagate with try (only inside a fallible function) or handle with catch.", Wrong: "let s = fs.read(p)", Right: "let s = try fs.read(p)\n// or\nlet s = fs.read(p) catch e { \"\" }"},
	"E0402": {Title: "try outside fallible function", Detail: "try propagates the error to the caller, so the function must be declared fallible with -> !T (or -> ! without a value). Otherwise use catch."},
	"E0403": {Title: "fail outside fallible function", Detail: "fail requires the function to be declared with -> !T."},
	"W0404": {Title: "nothing can fail", Detail: "try/catch is applied to an expression that cannot fail."},
	"E0405": {Title: "invalid fail value", Detail: "fail takes an Error (error(\"msg\", code: \"E_X\")) or a Str."},

	"E0501": {Title: "undeclared effect", Detail: "The function performs an effect (fs, net, env, proc, clock, rand) without declaring it in 'uses'. Effects are capabilities: the runner must grant them with --allow.", Wrong: "fn load() -> !Str { return try fs.read(\"a\") }", Right: "fn load() -> !Str uses fs { return try fs.read(\"a\") }"},
	"E0502": {Title: "unknown effect", Detail: "Effects are: clock, env, fs, net, proc, rand."},
	"W0503": {Title: "unused effect", Detail: "A declared effect is never used; remove it (least privilege)."},

	"E0601": {Title: "unknown struct field", Detail: "The struct literal names a field that does not exist."},
	"E0602": {Title: "missing struct field", Detail: "Fields without a default value and not optional must be given."},
	"E0603": {Title: "unknown variant", Detail: "The enum has no variant with this name."},
	"E0604": {Title: "variant arity", Detail: "A variant pattern must have one sub-pattern per field (use _ to ignore one)."},

	"E0701": {Title: "non-exhaustive match", Detail: "Every possible value must be matched. For enums list all variants (the message names the missing ones); otherwise add a final '_ => ...' arm."},
	"W0702": {Title: "unreachable arm", Detail: "An earlier arm already matches everything."},

	"E0801": {Title: "module not found", Detail: "A local import could not be read. Paths are relative to the importing file; the .pg extension is optional."},
	"E0802": {Title: "import cycle", Detail: "Modules import each other. Move shared declarations into a third module."},
	"E0803": {Title: "unknown module", Detail: "Not a standard module. Local modules start with ./ or ../"},

	"E0901": {Title: "break/continue outside loop", Detail: "break and continue are only valid inside for/while."},
	"E0903": {Title: "self outside method", Detail: "self is only available in impl methods that declare it as first parameter."},
	"E0904": {Title: "invalid main", Detail: "main takes no parameters (use os.args()) and returns nothing: fn main() or fn main() -> !."},
	"W0905": {Title: "unreachable code", Detail: "The statement follows a return/fail and never runs."},
	"W0907": {Title: "unused value", Detail: "An expression statement computes a value that is discarded (did you mean '=' instead of '=='?)."},

	"R0001": {Title: "runtime type error", Detail: "A value has the wrong type at runtime (argument, return, field or operand). Usually reported statically when types are known."},
	"R0002": {Title: "index out of range", Detail: "List/Str index outside 0..len-1. Negative indexes are not allowed; use xs.last() / xs.first() which return nil when empty."},
	"R0003": {Title: "missing map key", Detail: "m[k] on a missing key. Use m.get(k) ?? default or check m.has(k)."},
	"R0004": {Title: "division by zero", Detail: "Integer or float division/modulo by zero."},
	"R0005": {Title: "integer overflow", Detail: "Int arithmetic overflowed 64 bits (Pygo never wraps around)."},
	"R0006": {Title: "nil access", Detail: "A field or method was accessed on nil."},
	"R0007": {Title: "assertion failed", Detail: "An assert condition was false. 'values' reports the operands."},
	"R0008": {Title: "contract violated", Detail: "A requires (precondition) or ensures (postcondition) was false. 'values' reports the variables."},
	"R0009": {Title: "unhandled failure", Detail: "A failure reached a call site without try/catch, or escaped a non-fallible function."},
	"R0010": {Title: "capability denied", Detail: "The program used an effect that was not granted. Re-run with --allow <effect> (exit code 4)."},
	"R0011": {Title: "step budget exhausted", Detail: "The --max-steps limit was reached (likely an infinite loop)."},
	"R0012": {Title: "timeout", Detail: "The --timeout limit was reached."},
	"R0013": {Title: "explicit panic", Detail: "panic(...) was called."},
	"R0014": {Title: "unknown name at runtime", Detail: "A field, method or module member does not exist."},
	"R0015": {Title: "bad arguments", Detail: "Wrong number or names of arguments at runtime."},
	"R0016": {Title: "no match arm", Detail: "No arm of a match accepted the value."},
	"R0017": {Title: "channel misuse", Detail: "Send on a closed channel, or closing twice."},
	"R0099": {Title: "internal error", Detail: "A bug in the Pygo runtime. Please report it with the program."},
}

// Explain returns the explanation of code, if known.
func Explain(code string) (Explanation, bool) {
	e, ok := Explanations[code]
	e.Code = code
	return e, ok
}
