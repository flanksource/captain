package bash

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Scanner analyzes bash commands for safety violations
type Scanner struct {
	classifier *PathClassifier
	config     *Config
	cwd        string
}

// NewScanner creates a new bash scanner with configuration
func NewScanner(cwd string, config *Config) *Scanner {
	if config == nil {
		config = &Config{}
	}
	return &Scanner{
		classifier: NewPathClassifier(cwd, config),
		config:     config,
		cwd:        cwd,
	}
}

// Scan analyzes a bash command and returns violations
func (s *Scanner) Scan(command string) *ScanResult {
	result := &ScanResult{
		Allowed:        true,
		Violations:     []Violation{},
		SafeOperations: []string{},
	}

	if interpreter := DetectInterpreter(command); interpreter != "" {
		result.SafeOperations = append(result.SafeOperations,
			strings.ToLower(interpreter)+" (not analyzed)")
		return result
	}

	parser := syntax.NewParser()
	node, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		result.ParseError = err.Error()
		result.Allowed = false
		result.Reason = "Failed to parse bash command"
		result.Violations = append(result.Violations, Violation{
			Message: "Failed to parse bash command: " + err.Error(),
			Command: command,
		})
		return result
	}

	// Get file operations from existing analyzer
	analysisResult, _ := Analyze(command)
	if analysisResult != nil {
		result.Operations = analysisResult.Operations
	}

	s.walkNode(node, result)

	for _, v := range result.Violations {
		if v.Severity > result.OverallSeverity {
			result.OverallSeverity = v.Severity
		}
	}

	if len(result.Violations) > 0 {
		result.Allowed = false
		result.Reason = result.Violations[0].Message
	}

	return result
}

func (s *Scanner) walkNode(node syntax.Node, result *ScanResult) {
	if node == nil {
		return
	}

	switch n := node.(type) {
	case *syntax.File:
		if len(n.Stmts) > 1 {
			for _, stmt := range n.Stmts {
				for _, redir := range stmt.Redirs {
					if redir.Hdoc != nil {
						s.analyzeHeredoc(redir, result)
						return
					}
				}
			}
			result.Violations = append(result.Violations, CreateViolation(
				SeverityHigh, OpModify, ";", "",
				"B-1: compound command ';' is not allowed — split into separate Bash calls",
			))
			return
		}
		for _, stmt := range n.Stmts {
			s.walkNode(stmt, result)
		}
	case *syntax.Stmt:
		if n.Cmd != nil {
			s.walkNode(n.Cmd, result)
		}
		for _, redirect := range n.Redirs {
			s.analyzeRedirect(redirect, result)
		}
	case *syntax.CallExpr:
		s.analyzeCommand(n, result)
	case *syntax.BinaryCmd:
		if n.Op == syntax.AndStmt || n.Op == syntax.OrStmt {
			op := "&&"
			if n.Op == syntax.OrStmt {
				op = "||"
			}
			result.Violations = append(result.Violations, CreateViolation(
				SeverityHigh, OpModify, op, "",
				fmt.Sprintf("B-1: compound command '%s' is not allowed — split into separate Bash calls", op),
			))
			return
		}
		s.walkNode(n.X, result)
		s.walkNode(n.Y, result)
	case *syntax.Subshell:
		for _, stmt := range n.Stmts {
			s.walkNode(stmt, result)
		}
	case *syntax.Block:
		for _, stmt := range n.Stmts {
			s.walkNode(stmt, result)
		}
	case *syntax.IfClause:
		for _, stmt := range n.Cond {
			s.walkNode(stmt, result)
		}
		for _, stmt := range n.Then {
			s.walkNode(stmt, result)
		}
		if n.Else != nil {
			s.walkNode(n.Else, result)
		}
	case *syntax.WhileClause, *syntax.ForClause:
		syntax.Walk(n, func(node syntax.Node) bool {
			if stmt, ok := node.(*syntax.Stmt); ok {
				s.walkNode(stmt, result)
				return false
			}
			return true
		})
	}
}

func (s *Scanner) analyzeCommand(call *syntax.CallExpr, result *ScanResult) {
	if len(call.Args) == 0 {
		return
	}

	cmdName := s.getCommandName(call)
	args := s.getCommandArgs(call)

	if s.isWhitelisted(cmdName, args) {
		result.SafeOperations = append(result.SafeOperations, cmdName+" (whitelisted)")
		return
	}

	// Python scripts - stub for now
	if IsPythonCommand(cmdName) {
		// FIXME: implement Python script analysis
		result.SafeOperations = append(result.SafeOperations, cmdName+" (python - not analyzed)")
		return
	}

	if IsFindCommand(cmdName) {
		s.analyzeFindCommand(cmdName, args, result)
		return
	}

	if IsSafePipeCommand(cmdName) {
		result.SafeOperations = append(result.SafeOperations, cmdName+" (safe pipe)")
		return
	}

	if IsDevTool(cmdName, args) {
		result.SafeOperations = append(result.SafeOperations, cmdName+" (dev tool)")
		return
	}

	if IsNetworkCommand(cmdName) {
		result.Violations = append(result.Violations, Violation{
			Message:        "Network operation detected",
			Command:        cmdName,
			Recommendation: "Network operations require review. Ensure the endpoint is trusted and necessary.",
		})
		return
	}

	if IsPackageInstallCommand(cmdName, args) {
		result.Violations = append(result.Violations, Violation{
			Message:        "Package installation detected",
			Command:        cmdName,
			Recommendation: "Package installation modifies the system. Verify this is intentional and required.",
		})
		return
	}

	if IsDestructiveDelete(cmdName, args) {
		s.analyzeDeleteCommand(cmdName, args, result)
		return
	}

	if IsPermissionCommand(cmdName) {
		s.analyzePermissionCommand(cmdName, args, result)
		return
	}

	if IsArchiveExtract(cmdName, args) {
		s.analyzeArchiveCommand(cmdName, args, result)
		return
	}

	if isWrite, path := CheckFileWrite(cmdName, args); isWrite && path != "" {
		s.analyzeFileWrite(cmdName, path, result)
		return
	}

	result.SafeOperations = append(result.SafeOperations, cmdName)
}

func (s *Scanner) analyzeRedirect(redir *syntax.Redirect, result *ScanResult) {
	if redir == nil || redir.Word == nil {
		return
	}

	if redir.Hdoc != nil {
		s.analyzeHeredoc(redir, result)
		return
	}

	path := s.getWordValue(redir.Word)
	opStr := redir.Op.String()
	if opStr == ">" || opStr == ">>" || opStr == "&>" || opStr == "&>>" {
		s.analyzeFileWrite("redirect", path, result)
	}
}

func (s *Scanner) analyzeFileWrite(cmd string, path string, result *ScanResult) {
	classification := s.classifier.ClassifyPath(path)
	if classification.IsSafe {
		result.SafeOperations = append(result.SafeOperations, fmt.Sprintf("write to %s (%s)", path, classification.Reason))
	} else {
		result.Violations = append(result.Violations, Violation{
			Message:        fmt.Sprintf("File write to unsafe location: %s (%s)", path, classification.Reason),
			Command:        cmd,
			Path:           path,
			Recommendation: "Avoid writing to system directories. Use paths within your project or /tmp instead.",
		})
	}
}

func (s *Scanner) analyzeDeleteCommand(cmd string, args []string, result *ScanResult) {
	var paths []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			paths = append(paths, arg)
		}
	}

	for _, path := range paths {
		classification := s.classifier.ClassifyPath(path)
		if !classification.IsSafe {
			result.Violations = append(result.Violations, Violation{
				Message:        fmt.Sprintf("Destructive delete targeting: %s (%s)", path, classification.Reason),
				Command:        cmd,
				Path:           path,
				Recommendation: "Ensure you're operating in the correct directory. Consider using 'rm' without -f flag and verify the path.",
			})
		} else {
			result.SafeOperations = append(result.SafeOperations, fmt.Sprintf("delete %s (safe path)", path))
		}
	}
}

func (s *Scanner) analyzePermissionCommand(cmd string, args []string, result *ScanResult) {
	if len(args) > 0 {
		path := args[len(args)-1]
		classification := s.classifier.ClassifyPath(path)
		if !classification.IsSafe {
			result.Violations = append(result.Violations, Violation{
				Message:        fmt.Sprintf("Permission change on: %s (%s)", path, classification.Reason),
				Command:        cmd,
				Path:           path,
				Recommendation: "Permission changes on system files are dangerous. Ensure you have the correct path.",
			})
		}
	}
}

func (s *Scanner) analyzeArchiveCommand(cmd string, args []string, result *ScanResult) {
	var targetPath string
	for i, arg := range args {
		if arg == "-C" && i+1 < len(args) {
			targetPath = args[i+1]
			break
		}
	}
	if targetPath != "" {
		classification := s.classifier.ClassifyPath(targetPath)
		if !classification.IsSafe {
			result.Violations = append(result.Violations, Violation{
				Message: fmt.Sprintf("Archive extraction to unsafe location: %s (%s)", targetPath, classification.Reason),
				Command: cmd,
				Path:    targetPath,
			})
		}
	}
}

func (s *Scanner) analyzeFindCommand(cmd string, args []string, result *ScanResult) {
	execCmd, searchPath, hasExec := ExtractFindExecCommand(args)
	if !hasExec {
		result.SafeOperations = append(result.SafeOperations, "find (no -exec)")
		return
	}

	pathClass := s.classifier.ClassifyPath(searchPath)
	if len(execCmd) > 0 {
		execCmdName := execCmd[0]
		execArgs := execCmd[1:]
		contextMsg := fmt.Sprintf("In find -exec from %s: ", searchPath)

		if IsDestructiveDelete(execCmdName, execArgs) {
			if pathClass.IsSafe {
				result.SafeOperations = append(result.SafeOperations, fmt.Sprintf("find -exec %s (safe path)", execCmdName))
			} else {
				result.Violations = append(result.Violations, Violation{
					Message: fmt.Sprintf("%sDestructive delete command '%s' will execute on multiple files", contextMsg, execCmdName),
					Command: cmd,
					Path:    searchPath,
				})
			}
			return
		}

		if IsPermissionCommand(execCmdName) {
			if !pathClass.IsSafe {
				result.Violations = append(result.Violations, Violation{
					Message: fmt.Sprintf("%sPermission change command '%s' will execute on multiple files", contextMsg, execCmdName),
					Command: cmd,
					Path:    searchPath,
				})
			}
			return
		}

		if IsNetworkCommand(execCmdName) {
			result.Violations = append(result.Violations, Violation{
				Message: fmt.Sprintf("%sNetwork operation '%s' will execute on multiple files", contextMsg, execCmdName),
				Command: cmd,
				Path:    searchPath,
			})
			return
		}

		if IsSafePipeCommand(execCmdName) || IsDevTool(execCmdName, execArgs) {
			result.SafeOperations = append(result.SafeOperations, fmt.Sprintf("find -exec %s (safe operation)", execCmdName))
			return
		}

		result.SafeOperations = append(result.SafeOperations, fmt.Sprintf("find -exec %s (from %s)", execCmdName, searchPath))
	}
}

func (s *Scanner) analyzeHeredoc(_ *syntax.Redirect, result *ScanResult) {
	result.Violations = append(result.Violations, CreateViolation(
		SeverityHigh, OpModify, "heredoc", "",
		"B-2: heredocs are not allowed",
	))
}

func (s *Scanner) getCommandName(call *syntax.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	return s.getWordValue(call.Args[0])
}

func (s *Scanner) getCommandArgs(call *syntax.CallExpr) []string {
	if len(call.Args) <= 1 {
		return []string{}
	}
	args := make([]string, 0, len(call.Args)-1)
	for i := 1; i < len(call.Args); i++ {
		args = append(args, s.getWordValue(call.Args[i]))
	}
	return args
}

func (s *Scanner) getWordValue(word *syntax.Word) string {
	if word == nil {
		return ""
	}
	var buf strings.Builder
	for _, part := range word.Parts {
		s.appendPart(&buf, part)
	}
	return buf.String()
}

func (s *Scanner) appendPart(buf *strings.Builder, part syntax.WordPart) {
	switch p := part.(type) {
	case *syntax.Lit:
		buf.WriteString(p.Value)
	case *syntax.DblQuoted:
		for _, innerPart := range p.Parts {
			s.appendPart(buf, innerPart)
		}
	case *syntax.SglQuoted:
		buf.WriteString(p.Value)
	case *syntax.ParamExp:
		buf.WriteString("$" + p.Param.Value)
	case *syntax.CmdSubst:
		buf.WriteString("$()")
	}
}

func (s *Scanner) isWhitelisted(cmd string, args []string) bool {
	if s.config == nil || len(s.config.WhitelistedCommands) == 0 {
		return false
	}
	fullCmd := cmd
	if len(args) > 0 {
		fullCmd = cmd + " " + strings.Join(args, " ")
	}
	for _, whitelisted := range s.config.WhitelistedCommands {
		if whitelisted == cmd || whitelisted == fullCmd {
			return true
		}
	}
	return false
}
