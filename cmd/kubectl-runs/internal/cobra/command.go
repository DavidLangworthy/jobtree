package cobra

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Command is a minimal stand-in for spf13/cobra.Command used in this repository.
type Command struct {
	Use               string
	Short             string
	RunE              func(cmd *Command, args []string) error
	PersistentPreRunE func(cmd *Command, args []string) error
	Args              func(cmd *Command, args []string) error
	SilenceUsage      bool
	SilenceErrors     bool

	parent            *Command
	children          []*Command
	flagSet           *flag.FlagSet
	persistentFlagSet *flag.FlagSet
	args              []string
	out               io.Writer
	err               io.Writer
}

// AddCommand registers child commands.
func (c *Command) AddCommand(cmds ...*Command) {
	for _, child := range cmds {
		if child == nil {
			continue
		}
		child.parent = c
		c.children = append(c.children, child)
	}
}

// SetArgs overrides the arguments used when executing the command.
func (c *Command) SetArgs(args []string) { c.args = args }

// Flags returns the command-specific flag set.
func (c *Command) Flags() *flag.FlagSet {
	if c.flagSet == nil {
		fs := flag.NewFlagSet(c.Use, flag.ContinueOnError)
		fs.SetOutput(c.OutOrStderr())
		c.flagSet = fs
	}
	return c.flagSet
}

// PersistentFlags returns the persistent flag set (inherited by subcommands).
func (c *Command) PersistentFlags() *flag.FlagSet {
	if c.persistentFlagSet == nil {
		fs := flag.NewFlagSet(c.Use+"-persistent", flag.ContinueOnError)
		fs.SetOutput(c.OutOrStderr())
		c.persistentFlagSet = fs
	}
	return c.persistentFlagSet
}

// SetOut configures the stdout writer.
func (c *Command) SetOut(out io.Writer) { c.out = out }

// SetErr configures the stderr writer.
func (c *Command) SetErr(err io.Writer) { c.err = err }

// OutOrStdout returns stdout or a parent/default fallback.
func (c *Command) OutOrStdout() io.Writer {
	if c.out != nil {
		return c.out
	}
	if c.parent != nil {
		return c.parent.OutOrStdout()
	}
	return os.Stdout
}

// OutOrStderr returns stderr or a parent/default fallback.
func (c *Command) OutOrStderr() io.Writer {
	if c.err != nil {
		return c.err
	}
	if c.parent != nil {
		return c.parent.OutOrStderr()
	}
	return os.Stderr
}

// SetErr sets the error writer for compatibility with Cobra API.
// (The real Cobra exposes SetErr; here it simply assigns the writer.)

// Execute parses flags, resolves subcommands, and invokes RunE.
func (c *Command) Execute() error {
	args := c.args
	if args == nil {
		args = os.Args[1:]
	}
	rest, err := c.parseFlags(c.PersistentFlags(), args)
	if err != nil {
		return err
	}
	if err := c.execute(rest); err != nil {
		if c.SilenceErrors {
			return err
		}
		return err
	}
	return nil
}

func (c *Command) execute(args []string) error {
	if len(args) > 0 {
		name := args[0]
		if child := c.findSubcommand(name); child != nil {
			rest := args[1:]
			var err error
			if child.persistentFlagSet != nil {
				rest, err = child.parseFlags(child.persistentFlagSet, rest)
				if err != nil {
					return err
				}
			}
			if err := child.execute(rest); err != nil {
				return err
			}
			return nil
		}
	}

	rest, err := c.parseFlags(c.Flags(), args)
	if err != nil {
		return err
	}

	if c.Args != nil {
		if err := c.Args(c, rest); err != nil {
			return err
		}
	}

	if c.RunE == nil {
		if len(rest) > 0 {
			return fmt.Errorf("unknown command: %s", rest[0])
		}
		return nil
	}

	if err := c.runPersistent(rest); err != nil {
		return err
	}
	return c.RunE(c, rest)
}

func (c *Command) runPersistent(args []string) error {
	chain := c.ancestorChain()
	for _, cmd := range chain {
		if cmd.PersistentPreRunE != nil {
			if err := cmd.PersistentPreRunE(cmd, args); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Command) ancestorChain() []*Command {
	var chain []*Command
	current := c
	for current != nil {
		chain = append([]*Command{current}, chain...)
		current = current.parent
	}
	return chain
}

func (c *Command) findSubcommand(name string) *Command {
	for _, child := range c.children {
		if child == nil {
			continue
		}
		use := child.Use
		if idx := strings.Index(use, " "); idx >= 0 {
			use = use[:idx]
		}
		if use == name {
			return child
		}
	}
	return nil
}

func (c *Command) parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	if fs == nil {
		return args, nil
	}
	fs.SetOutput(c.OutOrStderr())
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.PrintDefaults()
			return nil, nil
		}
		return nil, err
	}
	return fs.Args(), nil
}

// ExactArgs ensures the command receives exactly n arguments.
func ExactArgs(n int) func(cmd *Command, args []string) error {
	return func(cmd *Command, args []string) error {
		if len(args) != n {
			return fmt.Errorf("expected %d args, got %d", n, len(args))
		}
		return nil
	}
}
