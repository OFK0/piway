package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Runner shows a plan and carries it out step by step.
type Runner struct {
	Host Host
	Out  io.Writer
	// In answers the questions; with Yes it is not read.
	In     *bufio.Reader
	Yes    bool // run every step without asking
	DryRun bool // print the plan, run nothing
}

// ErrDeclined means the user stopped setup at a question.
var ErrDeclined = errors.New("stopped; nothing after this was changed")

// Run prints the plan, then runs each step after confirmation. It stops at
// the first failure: the steps before it are done, and the next run skips
// them because doctor finds them in place. It reports whether a reboot is
// needed for a step to take effect.
func (r *Runner) Run(ctx context.Context, steps []Step) (reboot bool, err error) {
	if len(steps) == 0 {
		fmt.Fprintln(r.Out, "Nothing to do: everything is in place.")
		return false, nil
	}
	work := 0
	for _, s := range steps {
		if len(s.Actions) > 0 {
			work++
		}
	}
	fmt.Fprintf(r.Out, "The plan: %d %s.\n", work, plural(work, "step"))
	n := 0
	for _, s := range steps {
		if len(s.Actions) > 0 {
			n++
			fmt.Fprintf(r.Out, "  %d. %s\n", n, s.Title)
		}
	}
	if r.DryRun {
		fmt.Fprintln(r.Out, "\nDry run: these commands would run, in this order. Nothing was changed.")
	}

	n = 0
	for _, s := range steps {
		if len(s.Actions) == 0 {
			if s.Manual != "" {
				fmt.Fprintf(r.Out, "\nNote: %s\n", s.Manual)
			}
			continue
		}
		n++
		fmt.Fprintf(r.Out, "\n%d/%d  %s\n", n, work, s.Title)
		if s.Why != "" {
			fmt.Fprintf(r.Out, "      why: %s\n", s.Why)
		}
		for _, a := range s.Actions {
			fmt.Fprintf(r.Out, "      %s\n", strings.ReplaceAll(a.String(), "\n", "\n      "))
		}
		if r.DryRun {
			if s.Reboot {
				reboot = true
			}
			continue
		}
		if !r.Yes {
			ok, err := r.confirm("      Run this step? [Y/n] ")
			if err != nil {
				return reboot, err
			}
			if !ok {
				return reboot, ErrDeclined
			}
		}
		for _, a := range s.Actions {
			if err := r.do(ctx, a); err != nil {
				return reboot, fmt.Errorf("%s: %s: %w\nFix the problem above and run piway setup again; the steps already done are skipped", s.Title, a.String(), err)
			}
		}
		if s.Reboot {
			reboot = true
		}
		if s.Manual != "" {
			fmt.Fprintf(r.Out, "      next: %s\n", strings.ReplaceAll(s.Manual, "\n", "\n            "))
		}
	}
	return reboot, nil
}

func (r *Runner) do(ctx context.Context, a Action) error {
	switch {
	case a.Argv != nil:
		return r.Host.Exec(ctx, a.Argv, a.Stdin)
	case a.Path != "":
		return r.Host.WriteFile(a.Path, a.Content, a.Mode)
	case a.Func != nil:
		return a.Func(ctx)
	}
	return nil
}

// confirm reads a yes/no answer; an empty answer means yes.
func (r *Runner) confirm(prompt string) (bool, error) {
	fmt.Fprint(r.Out, prompt)
	line, err := r.In.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return false, fmt.Errorf("no answer (use --yes to run without questions): %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true, nil
	}
	return false, nil
}
