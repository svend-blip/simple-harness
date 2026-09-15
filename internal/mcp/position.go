package mcp

import (
	"strings"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// positionArgNames are the three attribution arguments the harness
// fills on behalf of the model: run_id, handoff_id and flow_key. Each
// argument reads its default from the SIMPLE_HARNESS_-prefixed
// environment variable carrying the upper-case form of the argument
// name (so the run_id argument reads SIMPLE_HARNESS_ + UPPER("run_id")
// = SIMPLE_HARNESS_<RUN_ID>). The harness reads its own position from
// the environment once per process invocation (os.Getenv; the
// environment of a harness process does not change while it runs), so
// a retrieval made through an MCP tool can always be attributed to the
// run that made it.
var positionArgNames = []string{"run_id", "handoff_id", "flow_key"}

// positionEnvName maps an argument name to its environment variable:
// the SIMPLE_HARNESS_-prefixed upper-case form of the name.
func positionEnvName(arg string) string {
	return "SIMPLE_HARNESS_" + strings.ToUpper(arg)
}

// applyPositionDefaults returns args with the harness's position
// filled in, without mutating args. For each of the three position
// arguments, the value is filled ONLY when all of
//
//   - (a) the tool's schema declares the property (Properties has the
//     name),
//   - (b) the caller's args has no entry for it, or its entry is the
//     empty string, and
//   - (c) the corresponding environment variable (looked up through
//     env) is non-empty.
//
// A value the model supplied is never replaced: an entry with a
// non-empty string, or any non-string value at all, is left exactly
// as-is. A property the tool does not declare is never added. The
// input map is never mutated — the fills land in a copy; a nil args
// with a declared property and a set variable yields a new map.
//
// The env parameter is the lookup seam: production passes os.Getenv
// (see mcpAdapter.Execute); tests pass a closure over a fixed table.
func applyPositionDefaults(schema tools.Schema, args map[string]any, env func(string) string) map[string]any {
	out := args
	filled := false
	for _, name := range positionArgNames {
		if _, declared := schema.Properties[name]; !declared {
			continue
		}
		if cur, present := args[name]; present {
			s, isString := cur.(string)
			if !isString || s != "" {
				// The model supplied a value (any non-empty
				// string, or a non-string JSON value): the
				// model's value wins.
				continue
			}
		}
		val := env(positionEnvName(name))
		if val == "" {
			continue
		}
		if !filled {
			cp := make(map[string]any, len(args)+len(positionArgNames))
			for k, v := range args {
				cp[k] = v
			}
			out = cp
			filled = true
		}
		out[name] = val
	}
	return out
}
