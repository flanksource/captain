package cmux

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Surface workspace discovery", func() {
	It("keeps each surface's current workspace UUID from the live tree", func() {
		surfaces, err := parseSurfaces([]byte(`{"windows":[{"workspaces":[
			{"id":"workspace-original","title":"Original","panes":[{"surfaces":[
				{"id":"surface-other","type":"terminal"}
			]}]},
			{"id":"workspace-current","title":"Current","panes":[{"surfaces":[
				{"id":"surface-moved","ref":"surface:42","title":"Agent","type":"terminal"}
			]}]}
		]}]}`))

		Expect(err).NotTo(HaveOccurred())
		Expect(surfaces).To(Equal(map[string]Surface{
			"surface-other": {ID: "surface-other", WorkspaceID: "workspace-original", Workspace: "Original", Type: "terminal"},
			"surface-moved": {ID: "surface-moved", Ref: "surface:42", Title: "Agent", WorkspaceID: "workspace-current", Workspace: "Current", Type: "terminal"},
		}))
	})
})
