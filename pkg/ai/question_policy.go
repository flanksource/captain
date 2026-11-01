package ai

import "fmt"

// QuestionInstructions describes the answer channel the host actually provides.
func QuestionInstructions(tool string, available bool) string {
	if available {
		return fmt.Sprintf("When clarification is required, use %s and wait for the answer before continuing. Do not ask clarification questions in your final response or end the turn while waiting for an answer.", tool)
	}
	return "Do not ask clarification questions. This run cannot receive answers through a native question tool. Complete the requested work independently using the available evidence and authorized scope. If a required decision cannot be resolved, report the blocker in your final response instead of asking a question or claiming completion."
}
