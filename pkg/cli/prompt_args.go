package cli

import (
	"reflect"

	"github.com/flanksource/clicky/flags"
	"github.com/spf13/cobra"
)

// ParsePromptArgs parses args with Captain's prompt flag set and returns the
// options plus the remaining positional arguments. A host command that embeds
// a Captain prompt (e.g. after --) uses it to keep the AI flags apart from its
// own arguments, with the same binding `captain prompt run` applies.
func ParsePromptArgs(args []string) (AIPromptOptions, []string, error) {
	var opts AIPromptOptions
	fields, err := flags.ParseStructFields(reflect.TypeOf(opts))
	if err != nil {
		return opts, nil, err
	}
	scratch := &cobra.Command{Use: "prompt"}
	var values []*flags.FlagValue
	for _, info := range fields {
		if fv := flags.BindFlag(scratch, info); fv != nil {
			values = append(values, fv)
		}
	}
	if err := scratch.Flags().Parse(args); err != nil {
		return opts, nil, err
	}
	value := reflect.ValueOf(&opts).Elem()
	for _, fv := range values {
		if err := flags.AssignFieldValue(value, fv, nil, false); err != nil {
			return opts, nil, err
		}
	}
	opts.AIRuntimeOptions = opts.WithChangedFlags(scratch.Flags())
	opts.PresetsSet = scratch.Flags().Changed("preset") || scratch.Flags().Changed("no-presets")
	opts.PromptRef = scratch.Flags().Lookup("prompt").Value.String()
	return opts, scratch.Flags().Args(), nil
}
