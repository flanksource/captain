// Pins that a captain command runs the tasks of one group at once, which a
// multi-model prompt run relies on to compare its runtimes in parallel.
package main

import (
	"io"
	"time"

	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

var _ = Describe("root task scheduling", func() {
	// Every command runs the root's PersistentPreRunE, which applies clicky's
	// task flags to the process-wide task manager; a multi-model prompt run
	// starts its runtimes as tasks of one group and needs them to run at once.
	It("runs a command's grouped tasks concurrently", func() {
		running := make(chan struct{}, 2)
		release := make(chan struct{})
		bothRan := false
		probe := &cobra.Command{Use: "concurrency-probe", RunE: func(*cobra.Command, []string) error {
			group := task.StartGroup[bool]("concurrency probe", task.WithConcurrency(2))
			tasks := make([]task.TypedTask[bool], 0, 2)
			for _, name := range []string{"first", "second"} {
				tasks = append(tasks, group.Add(name, func(flanksourceContext.Context, *task.Task) (bool, error) {
					running <- struct{}{}
					<-release
					return true, nil
				}))
			}
			defer close(release)
			for range 2 {
				select {
				case <-running:
				case <-time.After(5 * time.Second):
					return nil
				}
			}
			bothRan = true
			return nil
		}}
		root := newRootCommand()
		root.AddCommand(probe)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"concurrency-probe"})

		Expect(root.Execute()).To(Succeed())
		Expect(bothRan).To(BeTrue(), "the second task never started while the first was running")
	})
})
