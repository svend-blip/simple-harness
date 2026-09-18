package tools

import "context"

// workspaceKey is the context key under which Dispatch carries the
// active workspace to the tool it executes.
type workspaceKey struct{}

// WithWorkspace returns a context that carries ws. Dispatch calls it
// before Execute so a tool that needs the workspace root — the shell
// for its default working directory, the skill tools for their
// workspace search root — takes it from the harness rather than from
// os.Getwd(), which is only the workspace when the harness happens to
// have been launched from it.
func WithWorkspace(ctx context.Context, ws Workspace) context.Context {
	return context.WithValue(ctx, workspaceKey{}, ws)
}

// WorkspaceFromContext returns the workspace Dispatch attached, and
// whether one was attached at all.
func WorkspaceFromContext(ctx context.Context) (Workspace, bool) {
	ws, ok := ctx.Value(workspaceKey{}).(Workspace)
	return ws, ok
}
