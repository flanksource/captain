package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

var bashIcon = icons.Icon{Unicode: "💻", Iconify: "mdi:console", Style: "muted"}

type BashTool struct{ BaseTool }

func (t *BashTool) Name() string {
	if interp := t.interpreter(); interp != "" {
		return interp
	}
	return "Bash"
}

func (t *BashTool) Category() string { return "" }

func (t *BashTool) Pretty() api.Text {
	cmd := t.command()
	color := "text-green-400 font-medium"
	text := t.header(bashIcon, strings.ToLower(t.Name()), color)

	if timeout := t.Float("timeout"); timeout > 0 {
		text = text.Append(fmt.Sprintf(" (%ds)", int(timeout/1000)), "text-gray-500")
	}

	lang := "bash"
	if interp := t.interpreter(); interp != "" {
		lang = bash.InterpreterLanguage(interp)
		cmd = bash.ExtractInterpreterBody(cmd)
	}
	if i := strings.IndexByte(cmd, '\n'); i >= 0 {
		cmd = cmd[:i]
	}
	text = text.Space().Add(api.CodeBlock(lang, cmd).Trim())
	return text
}

func (t *BashTool) Detail() api.Textable {
	cmd := t.command()
	if cmd == "" {
		return nil
	}
	lang := "bash"
	if interp := t.interpreter(); interp != "" {
		lang = bash.InterpreterLanguage(interp)
		cmd = bash.ExtractInterpreterBody(cmd)
	}
	return api.NewCode(cmd, lang)
}

func (t *BashTool) FilePath() string { return "" }

func (t *BashTool) ExtractPath() string {
	cmd := t.command()
	if cmd == "" {
		return ""
	}
	result, err := bash.Analyze(cmd)
	if err != nil || len(result.ReferencedPaths) == 0 {
		return ""
	}
	return filepath.Dir(t.Rel(result.ReferencedPaths[0]))
}

func (t *BashTool) command() string {
	cmd := t.Str("command")
	if cmd != "" && t.ProjectRoot != "" {
		cmd = strings.ReplaceAll(cmd, t.ProjectRoot+"/", "")
	}
	return cmd
}

func (t *BashTool) interpreter() string {
	cmd := t.Str("command")
	if cmd == "" {
		return ""
	}
	return bash.DetectInterpreter(cmd)
}
