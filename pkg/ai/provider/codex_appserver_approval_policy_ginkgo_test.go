package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// codexWire connects c to an in-process app-server that answers every request
// with thread-1/turn-1 and records the params each method was sent with.
func codexWire(c *CodexAppServer) func(method string) map[string]any {
	toServerR, toServerW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	DeferCleanup(func() {
		cancel()
		_ = toServerW.Close()
		_ = toClientW.Close()
	})
	rpc := jsonrpc.New(toServerW, toClientR, true, jsonrpc.Handlers{})
	go func() { _ = rpc.Run(ctx) }()

	var mu sync.Mutex
	sent := map[string]map[string]any{}
	go func() {
		defer GinkgoRecover()
		scanner := bufio.NewScanner(toServerR)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			var frame struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params map[string]any  `json:"params"`
			}
			Expect(json.Unmarshal(scanner.Bytes(), &frame)).To(Succeed())
			mu.Lock()
			sent[frame.Method] = frame.Params
			mu.Unlock()
			reply, err := json.Marshal(map[string]any{"id": frame.ID, "result": map[string]any{
				"thread": map[string]any{"id": "thread-1"}, "turn": map[string]any{"id": "turn-1"},
			}})
			Expect(err).NotTo(HaveOccurred())
			if _, err := toClientW.Write(append(reply, '\n')); err != nil {
				return
			}
		}
	}()
	c.mu.Lock()
	c.rpc = rpc
	c.mu.Unlock()
	return func(method string) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return sent[method]
	}
}

// approvalPoliciesOnTheWire runs thread/start, thread/resume and turn/start for
// req and returns the approvalPolicy each one carried.
func approvalPoliciesOnTheWire(c *CodexAppServer, req ai.Request) []any {
	sent := codexWire(c)
	ctx := context.Background()
	_, err := c.startThread(ctx, req)
	Expect(err).NotTo(HaveOccurred())
	c.rememberThread("")
	resume := req
	resume.SessionID = "thread-1"
	_, err = c.startThread(ctx, resume)
	Expect(err).NotTo(HaveOccurred())
	_, err = c.startTurn(ctx, req, "thread-1", nil)
	Expect(err).NotTo(HaveOccurred())
	return []any{sent("thread/start")["approvalPolicy"], sent("thread/resume")["approvalPolicy"], sent("turn/start")["approvalPolicy"]}
}

var _ = Describe("Codex app-server approval policy", func() {
	granular := map[string]any{"granular": map[string]any{
		"sandbox_approval": true, "rules": true, "mcp_elicitations": true, "request_permissions": true, "skill_approval": false,
	}}
	approve := func(context.Context, api.ApprovalRequest) (api.ApprovalDecision, error) {
		return api.ApprovalDecision{Allow: true}, nil
	}

	DescribeTable("sends the granular policy only when a current callback will answer",
		func(onApproval api.ApprovalFunc, mode api.PermissionMode, want any) {
			c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}, OnApproval: onApproval})
			Expect(err).NotTo(HaveOccurred())
			request := ai.Request{Prompt: api.Prompt{User: "inspect"}, Permissions: api.Permissions{Mode: mode}}
			Expect(approvalPoliciesOnTheWire(c, request)).To(Equal([]any{want, want, want}))
		},
		Entry("an asking posture with a callback", api.ApprovalFunc(approve), api.PermissionDefault, granular),
		Entry("an asking posture without a callback", nil, api.PermissionDefault, "on-request"),
		Entry("never stays never with a callback", api.ApprovalFunc(approve), api.PermissionBypass, "never"),
	)

	It("keeps the string policy for a deprecated CanUseTool callback in legacy mode", func() {
		binDir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(binDir, "codex"), []byte("#!/bin/sh\n"), 0o755)).To(Succeed())
		DeferCleanup(os.Setenv, "PATH", os.Getenv("PATH"))
		Expect(os.Setenv("PATH", binDir)).To(Succeed())
		provider, err := api.NewProvider(api.Config{Model: api.Model{Name: "gpt-5.5", Mode: api.ModeAgent}, CanUseTool: approve})
		Expect(err).NotTo(HaveOccurred())
		c := provider.(*CodexAppServer)
		Expect(c.cfg.LegacyApprovals()).To(BeTrue())

		request := ai.Request{Prompt: api.Prompt{User: "inspect"}, Permissions: api.Permissions{Mode: api.PermissionDefault}}
		Expect(approvalPoliciesOnTheWire(c, request)).To(Equal([]any{"on-request", "on-request", "on-request"}))
	})
})
