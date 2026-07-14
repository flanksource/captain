package cli

import (
	"github.com/flanksource/captain/pkg/ai"
)

// WhoamiOptions and AdapterStatus live in pkg/ai: the adapter probe moved there
// so non-CLI consumers (the prompt --schema builder and the aichat server's
// model menu) can reuse it and its caching without importing pkg/cli. They are
// aliased here for the `captain whoami` command and its renderer.
type WhoamiOptions = ai.WhoamiOptions
type AdapterStatus = ai.AdapterStatus

// WhoamiResult is the command's render model: the probed adapters plus
// display-only knobs consumed by Pretty(). The knobs are never serialized.
type WhoamiResult struct {
	Adapters []AdapterStatus `json:"adapters"`

	sampleLimit int
	showModels  bool
}

func RunWhoami(opts WhoamiOptions) (any, error) {
	adapters, err := ai.ProbeAdapters(opts, ai.OSAuthProbe())
	if err != nil {
		return nil, err
	}
	return WhoamiResult{Adapters: adapters, sampleLimit: opts.Limit, showModels: opts.Models}, nil
}
