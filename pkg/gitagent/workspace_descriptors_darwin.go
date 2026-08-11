package gitagent

// markInheritedDescriptorsCloseOnExec prevents receive-pack's sideband pipes
// from surviving in the detached agent and keeping the dispatch push open.
func markInheritedDescriptorsCloseOnExec() error {
	return markOpenDescriptorsCloseOnExec("/dev/fd")
}
