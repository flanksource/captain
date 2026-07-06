package cli

import (
	"context"
	"os"
	"sort"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
	rpchttp "github.com/flanksource/clicky/rpc/http"
)

type liveSessionFile struct {
	source  string
	path    string
	modUnix int64
}

func discoverLiveSessionCandidates(ctx context.Context, cwd string, searchAll bool, source string, limit int) ([]sessionCandidate, error) {
	stopFind := rpchttp.Track(ctx, "find")
	files, err := discoverLiveSessionFiles(cwd, searchAll, source)
	stopFind()
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modUnix == files[j].modUnix {
			return files[i].path > files[j].path
		}
		return files[i].modUnix > files[j].modUnix
	})
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}

	refs := make([]sessionFileRef, len(files))
	for i, file := range files {
		refs[i] = sessionFileRef{source: file.source, path: file.path}
	}
	return summarizeSessionRefs(ctx, refs), nil
}

func discoverLiveSessionFiles(cwd string, searchAll bool, source string) ([]liveSessionFile, error) {
	var files []liveSessionFile
	if source == "all" || source == "claude" {
		claudeFiles, err := claude.FindSessionFiles(claude.GetProjectsDir(), cwd, searchAll)
		if err != nil {
			return nil, err
		}
		files = appendSessionFiles(files, "claude", claudeFiles)
	}
	if source == "all" || source == "codex" {
		codexFiles, err := history.FindCodexSessionFiles()
		if err != nil {
			return nil, err
		}
		matchRoot := cwd
		if cwd != "" {
			projectInfo := claude.FindProjectInfo(cwd)
			if projectInfo.Root != "" {
				matchRoot = projectInfo.Root
			}
		}
		for _, file := range codexFiles {
			if !searchAll {
				meta, err := history.ReadCodexSessionMeta(file)
				if err != nil || meta == nil || !codexMetaMatchesProject(meta, matchRoot) {
					continue
				}
			}
			files = appendSessionFiles(files, "codex", []string{file})
		}
	}
	return files, nil
}

func appendSessionFiles(out []liveSessionFile, source string, paths []string) []liveSessionFile {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		out = append(out, liveSessionFile{
			source:  source,
			path:    path,
			modUnix: info.ModTime().UnixNano(),
		})
	}
	return out
}
