package cmux

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TopSnapshot.Resolve", func() {
	var snapshot TopSnapshot

	BeforeEach(func() {
		snapshot = TopSnapshot{Windows: []TopWindow{{Workspaces: []TopWorkspace{
			{
				ID: "workspace-id", Ref: "workspace:3", Title: "Captain",
				Panes: []TopPane{
					{ID: "pane-one", Ref: "pane:5", Surfaces: []TopSurface{
						{ID: "surface-one", Ref: "surface:21", Title: "API", TTY: "/dev/ttys001", Resources: ProcessResources{PIDs: []int{101, 102}}},
						{ID: "surface-two", Ref: "surface:22", Title: "Tests", TTY: "/dev/ttys002", Resources: ProcessResources{PIDs: []int{102, 103}}},
					}},
					{ID: "pane-two", Ref: "pane:6", Surfaces: []TopSurface{
						{ID: "surface-three", Ref: "surface:23", Title: "Logs", TTY: "/dev/ttys003", Resources: ProcessResources{PIDs: []int{104}}},
					}},
				},
			},
		}}}}
	})

	It("resolves the most-specific compatible surface selector", func() {
		target, err := snapshot.Resolve(Selector{
			WorkspaceID: "workspace-id",
			PaneRef:     "pane:5",
			SurfaceID:   "surface-two",
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(target.Kind).To(Equal("surface"))
		Expect(target.PIDs).To(Equal([]int{102, 103}))
		Expect(target.Locations).To(HaveKeyWithValue(102, []ProcessLocation{{
			WorkspaceID: "workspace-id", WorkspaceRef: "workspace:3", WorkspaceTitle: "Captain",
			PaneID: "pane-one", PaneRef: "pane:5",
			SurfaceID: "surface-two", SurfaceRef: "surface:22", SurfaceTitle: "Tests", TTY: "/dev/ttys002",
		}}))
	})

	It("unions and deduplicates every process across a pane", func() {
		target, err := snapshot.Resolve(Selector{PaneID: "pane-one"})

		Expect(err).NotTo(HaveOccurred())
		Expect(target.Kind).To(Equal("pane"))
		Expect(target.PIDs).To(Equal([]int{101, 102, 103}))
		Expect(target.Locations[102]).To(HaveLen(2))
	})

	It("unions every surface process across a workspace", func() {
		target, err := snapshot.Resolve(Selector{WorkspaceRef: "workspace:3"})

		Expect(err).NotTo(HaveOccurred())
		Expect(target.Kind).To(Equal("workspace"))
		Expect(target.PIDs).To(Equal([]int{101, 102, 103, 104}))
	})

	It("rejects selector lines from different hierarchy branches", func() {
		_, err := snapshot.Resolve(Selector{PaneRef: "pane:6", SurfaceRef: "surface:21"})

		Expect(err).To(MatchError(ContainSubstring("does not identify a cmux target")))
	})
})
