package cli

import (
	clickyapi "github.com/flanksource/clicky/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// collectTreeIDs walks the rendered forest and returns every Captain ID in
// visit order, so a dropped subtree shows up as a missing ID rather than as
// silently empty output.
func collectTreeIDs(nodes []clickyapi.TreeNode) []string {
	ids := []string{}
	for _, node := range nodes {
		typed, ok := node.(sessionGetTreeNode)
		if !ok {
			continue
		}
		ids = append(ids, typed.item.CaptainID)
		ids = append(ids, collectTreeIDs(typed.GetChildren())...)
	}
	return ids
}

func treeChildren(result SessionGetResult) []clickyapi.TreeNode {
	branch, ok := result.Tree().(*clickyapi.ConcreteBranchNode)
	Expect(ok).To(BeTrue())
	return branch.Children
}

var _ = Describe("session get tree", func() {
	const (
		rootID        = "11de1d82-7622-494f-8219-3ca32bd13dff"
		orchestration = "2832bf9b-4aca-5bcd-8127-94eb8dc505d1"
		transcript    = "a255a8d1-a1c9-4da2-a420-51bbd2c8a561"
	)

	It("renders a mid-thread slice whose parents are all outside the result set", func() {
		// Resolving a provider session ID shared across sources returns the
		// orchestration row and the transcript row it parents; the thread root
		// is not in the slice, so no item has an empty parent.
		result := SessionGetResult{Sessions: []SessionGetItem{
			{CaptainID: transcript, ParentSessionID: orchestration, RootSessionID: rootID},
			{CaptainID: orchestration, ParentSessionID: rootID, RootSessionID: rootID},
		}, Total: 2}

		Expect(collectTreeIDs(treeChildren(result))).To(Equal([]string{orchestration, transcript}))
	})

	It("still nests children under a root present in the result set", func() {
		result := SessionGetResult{Sessions: []SessionGetItem{
			{CaptainID: rootID},
			{CaptainID: orchestration, ParentSessionID: rootID},
			{CaptainID: transcript, ParentSessionID: orchestration},
		}, Total: 3}

		children := treeChildren(result)

		Expect(children).To(HaveLen(1))
		Expect(collectTreeIDs(children)).To(Equal([]string{rootID, orchestration, transcript}))
	})

	It("renders every session when parents form a cycle", func() {
		result := SessionGetResult{Sessions: []SessionGetItem{
			{CaptainID: orchestration, ParentSessionID: transcript},
			{CaptainID: transcript, ParentSessionID: orchestration},
		}, Total: 2}

		// Both parents are present, so the cycle yields no natural root. The
		// render must still terminate and must not swallow either session.
		Expect(collectTreeIDs(treeChildren(result))).To(ConsistOf(orchestration, transcript))
	})
})
