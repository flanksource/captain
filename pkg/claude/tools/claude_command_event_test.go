package tools

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ClaudeCommand and GoalStatus renderers", func() {
	Describe("ClaudeCommandTool", func() {
		It("renders a slash-command invocation with an argument preview", func() {
			tool := NewTool(BaseTool{
				RawTool: "ClaudeCommand",
				Input: map[string]any{
					"event":           "claude_command",
					"scope":           "turn",
					"command_name":    "/goal",
					"command_message": "goal",
					"command_args":    "ship the docker build on PR #32",
				},
			})
			Expect(tool).To(BeAssignableToTypeOf(&ClaudeCommandTool{}))
			Expect(tool.Name()).To(Equal("ClaudeCommand"))
			Expect(tool.Category()).To(Equal("chat"))

			pretty := tool.Pretty().String()
			Expect(pretty).To(ContainSubstring("/goal"))
			Expect(pretty).To(ContainSubstring("ship the docker build on PR #32"))

			detail := tool.Detail()
			Expect(detail).NotTo(BeNil())
			Expect(detail.String()).To(ContainSubstring("ship the docker build on PR #32"))
			Expect(detail.String()).NotTo(ContainSubstring("<command-args>"))
		})

		It("renders stdout output with the stream label and preview, wrapper-free", func() {
			tool := NewTool(BaseTool{
				RawTool: "ClaudeCommand",
				Input: map[string]any{
					"event":   "claude_command_output",
					"scope":   "turn",
					"stream":  "stdout",
					"content": "Goal set: ship it",
				},
			})
			Expect(tool).To(BeAssignableToTypeOf(&ClaudeCommandTool{}))

			pretty := tool.Pretty().String()
			Expect(pretty).To(ContainSubstring("stdout"))
			Expect(pretty).To(ContainSubstring("Goal set: ship it"))
			Expect(pretty).NotTo(ContainSubstring("<local-command-stdout>"))

			detail := tool.Detail()
			Expect(detail).NotTo(BeNil())
			Expect(detail.String()).To(ContainSubstring("Goal set: ship it"))
			Expect(detail.String()).NotTo(ContainSubstring("<local-command-stdout>"))
		})

		It("renders stderr output with the stderr stream label", func() {
			tool := NewTool(BaseTool{
				RawTool: "ClaudeCommand",
				Input: map[string]any{
					"event":   "claude_command_output",
					"scope":   "turn",
					"stream":  "stderr",
					"content": "command failed",
				},
			})
			pretty := tool.Pretty().String()
			Expect(pretty).To(ContainSubstring("stderr"))
			Expect(pretty).To(ContainSubstring("command failed"))
		})
	})

	Describe("GoalStatusTool", func() {
		It("renders an active goal with a condition preview", func() {
			tool := NewTool(BaseTool{
				RawTool: "GoalStatus",
				Input: map[string]any{
					"event":     "goal_status",
					"scope":     "session",
					"met":       false,
					"sentinel":  true,
					"condition": "release once CI is green",
				},
			})
			Expect(tool).To(BeAssignableToTypeOf(&GoalStatusTool{}))
			Expect(tool.Name()).To(Equal("GoalStatus"))
			Expect(tool.Category()).To(Equal("chat"))

			pretty := tool.Pretty().String()
			Expect(pretty).To(ContainSubstring("goal active"))
			Expect(pretty).To(ContainSubstring("release once CI is green"))

			detail := tool.Detail()
			Expect(detail).NotTo(BeNil())
			Expect(detail.String()).To(ContainSubstring("release once CI is green"))
		})

		It("renders a met goal", func() {
			tool := NewTool(BaseTool{
				RawTool: "GoalStatus",
				Input: map[string]any{
					"event":     "goal_status",
					"met":       true,
					"condition": "release once CI is green",
				},
			})
			Expect(tool.Pretty().String()).To(ContainSubstring("goal met"))
		})

		It("includes a failure reason in the detail", func() {
			tool := NewTool(BaseTool{
				RawTool: "GoalStatus",
				Input: map[string]any{
					"event":     "goal_status",
					"met":       false,
					"condition": "release once CI is green",
					"reason":    "docker build still failing",
				},
			})
			detail := tool.Detail()
			Expect(detail).NotTo(BeNil())
			Expect(detail.String()).To(ContainSubstring("docker build still failing"))
		})
	})
})
