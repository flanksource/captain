package bash

import "strings"

var safePipeCommands = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true,
	"awk": true, "gawk": true,
	"sed":  true,
	"head": true, "tail": true,
	"wc":   true,
	"sort": true, "uniq": true,
	"cut": true, "tr": true,
	"jq": true, "yq": true,
	"cat":    true,
	"tee":    true,
	"xargs":  true,
	"column": true, "paste": true, "join": true,
}

var destructiveDeleteCommands = map[string]bool{
	"rm":    true,
	"rmdir": true,
}

var networkCommands = map[string]bool{
	"curl": true, "wget": true,
	"nc": true, "netcat": true,
	"ssh": true, "scp": true,
	"rsync": true,
	"ftp":   true, "sftp": true,
}

var permissionCommands = map[string]bool{
	"chmod": true, "chown": true, "chgrp": true,
}

var packageInstallCommands = map[string]bool{
	"apt": true, "apt-get": true,
	"brew": true,
	"npm":  true, "yarn": true, "pnpm": true,
	"pip": true, "pip3": true,
	"go":  true,
	"gem": true, "bundle": true,
}

var archiveCommands = map[string]bool{
	"tar": true, "unzip": true, "gunzip": true,
	"untar": true, "extract": true,
}

var devToolCommands = map[string]bool{
	"make": true, "cmake": true,
	"go":     true,
	"npm":    true,
	"yarn":   true,
	"pytest": true, "jest": true, "mocha": true,
	"cargo": true, "rustc": true,
	"mvn": true, "gradle": true,
	"task": true,
}

// IsSafePipeCommand checks if a command is safe in a pipeline
func IsSafePipeCommand(cmd string) bool {
	return safePipeCommands[cmd]
}

// IsDestructiveDelete checks if a delete command has dangerous flags
func IsDestructiveDelete(cmd string, args []string) bool {
	if !destructiveDeleteCommands[cmd] {
		return false
	}
	for _, arg := range args {
		if strings.Contains(arg, "r") && strings.Contains(arg, "f") {
			return true
		}
		if arg == "-f" || arg == "--force" {
			return true
		}
	}
	return false
}

// IsNetworkCommand checks if a command performs network operations
func IsNetworkCommand(cmd string) bool {
	return networkCommands[cmd]
}

// IsPermissionCommand checks if a command modifies permissions
func IsPermissionCommand(cmd string) bool {
	return permissionCommands[cmd]
}

// IsPackageInstallCommand checks if a command installs packages
func IsPackageInstallCommand(cmd string, args []string) bool {
	if !packageInstallCommands[cmd] {
		return false
	}
	switch cmd {
	case "npm", "yarn", "pnpm":
		return len(args) > 0 && args[0] == "install"
	case "go":
		return len(args) > 0 && args[0] == "install"
	case "pip", "pip3":
		return len(args) > 0 && args[0] == "install"
	default:
		return true
	}
}

// IsArchiveExtract checks if an archive command is extracting
func IsArchiveExtract(cmd string, args []string) bool {
	if !archiveCommands[cmd] {
		return false
	}
	switch cmd {
	case "tar":
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") && strings.Contains(arg, "x") {
				return true
			}
		}
	case "unzip", "gunzip", "untar":
		return true
	}
	return false
}

// IsDevTool checks if a command is a common development tool
func IsDevTool(cmd string, args []string) bool {
	if !devToolCommands[cmd] {
		return false
	}
	switch cmd {
	case "npm", "yarn", "pnpm":
		if len(args) > 0 && args[0] == "install" {
			return false
		}
		return true
	case "go":
		if len(args) > 0 && args[0] == "install" {
			return false
		}
		return true
	default:
		return true
	}
}

// CheckFileWrite checks if a command writes to a file
func CheckFileWrite(cmd string, args []string) (bool, string) {
	writeCommands := map[string]bool{
		"echo": true, "printf": true,
		"cat": true, "tee": true,
		"touch": true,
		"cp":    true, "mv": true,
		"dd": true,
	}
	if !writeCommands[cmd] {
		return false, ""
	}
	if (cmd == "cp" || cmd == "mv") && len(args) > 1 {
		return true, args[len(args)-1]
	}
	if cmd == "touch" && len(args) > 0 {
		return true, args[0]
	}
	return true, ""
}

// IsPythonCommand checks if a command is a Python interpreter
func IsPythonCommand(cmd string) bool {
	return cmd == "python" || cmd == "python3" || cmd == "python2" ||
		strings.HasPrefix(cmd, "python3.") || strings.HasPrefix(cmd, "python2.")
}

// IsFindCommand checks if a command is find
func IsFindCommand(cmd string) bool {
	return cmd == "find"
}

// ExtractFindExecCommand extracts the -exec command and search path from find arguments
func ExtractFindExecCommand(args []string) (execCmd []string, searchPath string, hasExec bool) {
	if len(args) == 0 {
		return nil, "", false
	}

	searchPath = "."
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		searchPath = args[0]
	}

	execIndex := -1
	for i, arg := range args {
		if arg == "-exec" || arg == "-execdir" {
			execIndex = i
			break
		}
	}

	if execIndex == -1 {
		return nil, searchPath, false
	}

	for i := execIndex + 1; i < len(args); i++ {
		arg := args[i]
		if arg == ";" || arg == "+" || arg == "\\;" {
			break
		}
		if arg == "{}" {
			arg = "FIND_RESULT"
		}
		execCmd = append(execCmd, arg)
	}

	return execCmd, searchPath, len(execCmd) > 0
}
