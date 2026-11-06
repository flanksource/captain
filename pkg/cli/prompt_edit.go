package cli

import "context"

type PromptSaveOptions struct {
	Directory string
	Reference string
	Write     PromptWriteRequest
}

func DiscoverPromptSources(ctx context.Context, directory string) ([]PromptSourceInfo, error) {
	ctx, err := PromptContext(ctx, directory)
	if err != nil {
		return nil, err
	}
	return promptSourceInfos(ctx)
}

func SavePrompt(ctx context.Context, options PromptSaveOptions) (PromptDetail, error) {
	ctx, err := PromptContext(ctx, options.Directory)
	if err != nil {
		return PromptDetail{}, err
	}
	if options.Reference == "" {
		return writeNewLocalPrompt(ctx, options.Write)
	}
	return updatePrompt(ctx, options.Reference, map[string]any{
		"content": options.Write.Content, "baseVersion": options.Write.BaseVersion,
	})
}

type PromptRemoveOptions struct {
	Directory string
	Reference string
	Version   string
}

func RemovePrompt(ctx context.Context, options PromptRemoveOptions) error {
	ctx, err := PromptContext(ctx, options.Directory)
	if err != nil {
		return err
	}
	record, err := resolvePromptRecord(ctx, promptRecordOptions{ID: options.Reference})
	if err != nil {
		return err
	}
	if err := checkPromptBaseVersion(record, options.Version); err != nil {
		return err
	}
	return deletePrompt(ctx, options.Reference)
}
