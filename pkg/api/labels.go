package api

import (
	"slices"
	"strings"
)

// RuntimeOwnedLabels are the task-label keys a provider derives from the run it
// is about to make, rather than from anything the caller can know at submit
// time.
//
// A caller naming one of these is not contributing identity, it is contradicting
// it: a task row claiming a budget the run does not enforce, or a session it is
// not resuming, is worse than one that says nothing. They are therefore dropped
// from the caller's half rather than merged, and each provider adds back the
// value it actually resolved.
var RuntimeOwnedLabels = []string{"model", "provider", "mode", "runSession", "budget"}

// HostLabels is the caller's half of a supervised task's identity: Labels with
// blanks and the runtime-owned keys removed, ready for a provider to add its own
// facts to. The result is always a fresh map, so a provider may write into it
// without reaching back into the request.
func (s Spec) HostLabels() map[string]string {
	labels := make(map[string]string, len(s.Labels))
	for key, value := range s.Labels {
		value = strings.TrimSpace(value)
		if value == "" || slices.Contains(RuntimeOwnedLabels, key) {
			continue
		}
		labels[key] = value
	}
	return labels
}

// HostLabel reads one caller-supplied label — the conventional ones being
// "title" (what the run is called) and "href" (where to read more about it).
// A runtime-owned key reads as empty here for the same reason HostLabels drops
// it: the caller does not get to answer it.
func (s Spec) HostLabel(key string) string {
	if slices.Contains(RuntimeOwnedLabels, key) {
		return ""
	}
	return strings.TrimSpace(s.Labels[key])
}
