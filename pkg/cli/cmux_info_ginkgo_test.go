package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/flanksource/captain/pkg/cmux"
	gopsnet "github.com/shirou/gopsutil/v3/net"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("cmux info selectors", func() {
	DescribeTable("parses valid input",
		func(input []string, expected cmuxInfoSelector) {
			actual, err := parseCmuxInfoSelector(input)
			Expect(err).NotTo(HaveOccurred())
			Expect(actual).To(Equal(expected))
		},
		Entry("bare PID", []string{"33745"}, cmuxInfoSelector{PID: 33745}),
		Entry("separate Copy IDs lines", []string{
			"workspace_ref=workspace:3",
			"pane_id=pane-id",
			"surface_ref=surface:21",
		}, cmuxInfoSelector{Selector: cmux.Selector{
			WorkspaceRef: "workspace:3",
			PaneID:       "pane-id",
			SurfaceRef:   "surface:21",
		}}),
		Entry("quoted multiline Copy IDs", []string{"pane_ref=pane:5\nsurface_id=surface-id"}, cmuxInfoSelector{Selector: cmux.Selector{
			PaneRef:   "pane:5",
			SurfaceID: "surface-id",
		}}),
	)

	DescribeTable("rejects invalid input",
		func(input []string, message string) {
			_, err := parseCmuxInfoSelector(input)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("empty input", nil, "requires cmux Copy IDs lines or a PID"),
		Entry("unknown field", []string{"tab_id=tab-id"}, "unknown cmux selector key"),
		Entry("conflicting fields", []string{"pane_ref=pane:5", "pane_ref=pane:6"}, "conflicting pane_ref"),
		Entry("PID mixed with selector", []string{"33745", "pane_ref=pane:5"}, "cannot mix a PID"),
		Entry("non-positive PID", []string{"0"}, "PID must be positive"),
	)
})

var _ = Describe("process runtime detection", func() {
	DescribeTable("classifies executables",
		func(executable, name, expected string) {
			Expect(detectProcessRuntime(executable, name)).To(Equal(expected))
		},
		Entry("Node", "/usr/local/bin/node", "node", "node"),
		Entry("Bun", "/usr/local/bin/bun", "bun", "bun"),
		Entry("Deno", "/usr/local/bin/deno", "deno", "deno"),
		Entry("Python", "/usr/bin/python3.13", "Python", "python"),
		Entry("Java", "/usr/bin/java", "java", "java"),
		Entry("Ruby", "/usr/bin/ruby", "ruby", "ruby"),
		Entry("shell", "/bin/zsh", "zsh", "shell"),
		Entry("native", "/usr/bin/git", "git", "native"),
		Entry("unknown", "", "", "unknown"),
	)

	It("detects Go build information before executable-name heuristics", func() {
		executable, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		Expect(detectProcessRuntime(executable, "node")).To(Equal("go"))
	})

	It("reports only listening TCP endpoints in stable order", func() {
		connections := []gopsnet.ConnectionStat{
			{Type: syscall.SOCK_DGRAM, Laddr: gopsnet.Addr{IP: "127.0.0.1", Port: 5353}},
			{Type: syscall.SOCK_STREAM, Status: "ESTABLISHED", Laddr: gopsnet.Addr{IP: "127.0.0.1", Port: 443}},
			{Type: syscall.SOCK_STREAM, Status: "LISTEN", Laddr: gopsnet.Addr{IP: "::1", Port: 9090}},
			{Type: syscall.SOCK_STREAM, Status: "LISTEN", Laddr: gopsnet.Addr{IP: "127.0.0.1", Port: 8080}},
		}

		Expect(listeningTCPEndpoints(connections)).To(Equal([]CmuxListener{
			{Protocol: "tcp4", Address: "127.0.0.1", Port: 8080},
			{Protocol: "tcp6", Address: "::1", Port: 9090},
		}))
	})

	It("inspects the current Go process", func() {
		row, err := inspectProcess(context.Background(), os.Getpid())

		Expect(err).NotTo(HaveOccurred())
		Expect(row).To(SatisfyAll(
			HaveField("PID", os.Getpid()),
			HaveField("PPID", BeNumerically(">", 0)),
			HaveField("Executable", Not(BeEmpty())),
			HaveField("Runtime", "go"),
			HaveField("RSSBytes", BeNumerically(">", 0)),
		))
	})
})

var _ = Describe("RunCmuxInfo", func() {
	BeforeEach(func() {
		originalTop := loadCmuxTop
		originalInspect := inspectCmuxPID
		originalStack := loadGoStack
		DeferCleanup(func() {
			loadCmuxTop = originalTop
			inspectCmuxPID = originalInspect
			loadGoStack = originalStack
		})
	})

	It("inspects every resolved process in PID order and preserves locations", func() {
		loadCmuxTop = func() (cmux.TopSnapshot, error) {
			return cmux.TopSnapshot{Windows: []cmux.TopWindow{{Workspaces: []cmux.TopWorkspace{{
				ID: "workspace-id", Ref: "workspace:3", Panes: []cmux.TopPane{{
					ID: "pane-id", Ref: "pane:5", Surfaces: []cmux.TopSurface{{
						ID: "surface-id", Ref: "surface:21", Resources: cmux.ProcessResources{PIDs: []int{202, 101}},
					}},
				}},
			}}}}}, nil
		}
		inspectCmuxPID = func(_ context.Context, pid int) (CmuxProcess, error) {
			return CmuxProcess{PID: pid, Runtime: "native"}, nil
		}

		result, err := RunCmuxInfo(context.Background(), CmuxInfoOptions{Input: []string{"surface_ref=surface:21"}})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Target.Kind).To(Equal("surface"))
		Expect(result.Processes).To(HaveLen(2))
		Expect([]int{result.Processes[0].PID, result.Processes[1].PID}).To(Equal([]int{101, 202}))
		Expect(result.Processes[0].Locations).To(HaveLen(1))
	})

	It("captures stacks only for Go processes and keeps gops failures on the row", func() {
		loadCmuxTop = func() (cmux.TopSnapshot, error) {
			return cmux.TopSnapshot{Windows: []cmux.TopWindow{{Workspaces: []cmux.TopWorkspace{{
				ID: "workspace-id", Ref: "workspace:3", Panes: []cmux.TopPane{{
					ID: "pane-id", Ref: "pane:5", Surfaces: []cmux.TopSurface{{
						ID: "surface-id", Ref: "surface:21", Resources: cmux.ProcessResources{PIDs: []int{101, 202}},
					}},
				}},
			}}}}}, nil
		}
		inspectCmuxPID = func(_ context.Context, pid int) (CmuxProcess, error) {
			runtime := "node"
			if pid == 101 {
				runtime = "go"
			}
			return CmuxProcess{PID: pid, Runtime: runtime}, nil
		}
		loadGoStack = func(_ context.Context, pid int) (string, error) {
			Expect(pid).To(Equal(101))
			return "", errors.New("gops agent is unavailable")
		}

		result, err := RunCmuxInfo(context.Background(), CmuxInfoOptions{Input: []string{"surface_ref=surface:21"}, Stack: true})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Processes[0].StackError).To(Equal("gops agent is unavailable"))
		Expect(result.Processes[1].StackError).To(BeEmpty())
	})

	It("keeps processes that disappear during inspection", func() {
		inspectCmuxPID = func(_ context.Context, pid int) (CmuxProcess, error) {
			return CmuxProcess{PID: pid}, errors.New("process no longer exists")
		}

		result, err := RunCmuxInfo(context.Background(), CmuxInfoOptions{Input: []string{"101"}})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Processes[0].InspectionError).To(Equal("process no longer exists"))
	})

	It("fails when a resolved cmux target has no processes", func() {
		loadCmuxTop = func() (cmux.TopSnapshot, error) {
			return cmux.TopSnapshot{Windows: []cmux.TopWindow{{Workspaces: []cmux.TopWorkspace{{
				ID: "workspace-id", Ref: "workspace:3",
			}}}}}, nil
		}

		_, err := RunCmuxInfo(context.Background(), CmuxInfoOptions{Input: []string{"workspace_ref=workspace:3"}})

		Expect(err).To(MatchError(ContainSubstring("has no running processes")))
	})

	It("renders typed rows with detail-only diagnostics", func() {
		row := CmuxProcess{
			PID: 101, PPID: 50, Name: "captain", Runtime: "go", CPUPercent: 1.25, RSSBytes: 64 * 1024 * 1024,
			Executable: "/opt/bin/captain", Command: "captain serve", Stack: "goroutine 1 [running]", StackError: "",
			Listeners: []CmuxListener{{Protocol: "tcp4", Address: "127.0.0.1", Port: 8080}},
			Locations: []cmux.ProcessLocation{{SurfaceRef: "surface:21", SurfaceTitle: "API"}},
		}

		Expect(row.Row()).To(HaveKeyWithValue("pid", "101"))
		Expect(row.Row()).To(HaveKey("listeners"))
		Expect(row.RowDetail().String()).To(ContainSubstring("/opt/bin/captain"))
		Expect(row.RowDetail().String()).To(ContainSubstring("goroutine 1"))
		Expect(CmuxInfoResult{Processes: []CmuxProcess{row}}.Pretty().String()).To(ContainSubstring("captain"))
	})

	It("keeps zero-valued resource metrics and omits PID selectors in JSON", func() {
		payload, err := json.Marshal(CmuxInfoResult{
			Target:    CmuxInfoTarget{Kind: "pid", PID: 101},
			Processes: []CmuxProcess{{PID: 101, Listeners: []CmuxListener{}, Locations: []cmux.ProcessLocation{}}},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(payload).To(MatchJSON(`{
			"target":{"kind":"pid","pid":101},
			"processes":[{"pid":101,"cpu_percent":0,"rss_bytes":0,"listeners":[],"locations":[]}]
		}`))
	})

	It("bounds gops stack collection", func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		_, err := captureGoStack(ctx, 101)
		Expect(err).To(HaveOccurred())
	})
})
