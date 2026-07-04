package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestCompletionsReflectRealCommandTree proves the completions are generated
// from the live Cobra tree rather than a hand-written, silently-stale map: a
// throwaway subcommand added after construction must be discoverable
// without any change to completions.go. Bash's generated script statically
// enumerates every subcommand name, so it is checked by substring; zsh and
// fish's generated scripts instead dispatch dynamically through Cobra's
// hidden `__complete` command at completion time, so those are checked by
// actually invoking that mechanism — the same path a real shell takes when
// the user presses TAB.
func TestCompletionsReflectRealCommandTree(t *testing.T) {
	const throwaway = "zzz-throwaway-subcommand"

	newRootWithThrowaway := func() *cobra.Command {
		root := NewRootCommand()
		root.AddCommand(&cobra.Command{
			Use: throwaway,
			Run: func(*cobra.Command, []string) {},
		})
		return root
	}

	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			root := newRootWithThrowaway()
			var buf bytes.Buffer
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs([]string{"completions", shell})
			if err := root.Execute(); err != nil {
				t.Fatalf("completions %s: %v", shell, err)
			}
			if buf.Len() == 0 {
				t.Fatalf("expected non-empty %s completion script", shell)
			}
			if shell == "bash" && !strings.Contains(buf.String(), throwaway) {
				t.Fatalf("expected bash completion output to mention %q, got:\n%s", throwaway, buf.String())
			}
		})
	}

	t.Run("dynamic dispatch", func(t *testing.T) {
		root := newRootWithThrowaway()
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs([]string{cobra.ShellCompRequestCmd, ""})
		if err := root.Execute(); err != nil {
			t.Fatalf("__complete: %v", err)
		}
		if !strings.Contains(buf.String(), throwaway) {
			t.Fatalf("expected dynamic completion candidates to include %q, got:\n%s", throwaway, buf.String())
		}
	})
}

// TestCompletionsRejectsUnknownShell keeps the honest error path for a shell
// none of the three generators support.
func TestCompletionsRejectsUnknownShell(t *testing.T) {
	root := NewRootCommand()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"completions", "powershell"})
	if err := root.Execute(); err == nil {
		t.Fatalf("expected an error for an unsupported shell")
	}
}
