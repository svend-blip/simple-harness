package builtins

// intArg returns the integer carried by a tool argument.
//
// Arguments reach a tool by two routes: Go-constructed calls (tests,
// direct Execute callers) carry a literal int; JSON-decoded calls from the
// model carry float64, because encoding/json decodes every number as
// float64. The schema validator accepts a whole float64 for TypeInt, so a
// tool that then checks only for int rejects a call the schema has just
// approved. On 2026-09-02 every `read_file` with a `start_line` from a
// MiniMax session failed with "start_line is not an int" for exactly this
// reason, costing a turn per ranged read. A fractional float64 is still
// rejected: the schema promised an integer.
func intArg(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int64(n)) {
			return int(n), true
		}
		return 0, false
	default:
		return 0, false
	}
}
