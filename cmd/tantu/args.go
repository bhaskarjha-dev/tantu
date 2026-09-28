package main

import (
	"path/filepath"
	"strings"
)

// valueFlags lists command-line flags that expect a following value argument when not using '='.
var valueFlags = map[string]bool{
	"-transport":           true,
	"--transport":          true,
	"-t":                   true,
	"-listen":              true,
	"--listen":             true,
	"-l":                   true,
	"-peer":                true,
	"--peer":               true,
	"-p":                   true,
	"-port":                true,
	"--port":               true,
	"-web-port":            true,
	"--web-port":           true,
	"-web-addr":            true,
	"--web-addr":           true,
	"-store-dir":           true,
	"--store-dir":          true,
	"-output-dir":          true,
	"--output-dir":         true,
	"-o":                   true,
	"-timeout":             true,
	"--timeout":            true,
	"-ssh-host-key":        true,
	"--ssh-host-key":       true,
	"-ssh-authorized-key":  true,
	"--ssh-authorized-key": true,
	"-ssh-key":             true,
	"--ssh-key":            true,
	"-ssh-user":            true,
	"--ssh-user":           true,
	"-ssh-host":            true,
	"--ssh-host":           true,
	"-ssh-port":            true,
	"--ssh-port":           true,
	"-bridge":              true,
	"--bridge":             true,
	"-b":                   true,
	"-fingerprint":         true,
	"--fingerprint":        true,
	"-f":                   true,
	"-name":                true,
	"--name":               true,
	"-n":                   true,

	// Kept adjacent to the rest of the map deliberately: this list is a
	// reordering aid, so a value-taking flag that is missing here silently
	// loses its argument when it appears after a positional. TestSendValueFlagMapIsComplete
	// derives the real set from the flag sets and fails when they diverge.
	"-bundle-path":      true,
	"--bundle-path":     true,
	"-idempotency-key":  true,
	"--idempotency-key": true,
	"-ssh-fingerprint":  true,
	"--ssh-fingerprint": true,
	"-ssh-known-hosts":  true,
	"--ssh-known-hosts": true,
}

func isValueFlag(f string) bool {
	return valueFlags[f]
}

// sendHasPathHint reports whether a bare send argument looks like a filesystem
// path rather than text the user wants transmitted. A hint means a failed
// os.Stat is a user error that must be reported, not text to send silently.
//
// The decision is factored out so the classification is directly testable and
// so the `--text` override is honoured in exactly one place.
func sendHasPathHint(arg string, forceText bool) bool {
	if forceText {
		return false
	}
	return strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, ".\\") ||
		strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "\\") ||
		strings.HasPrefix(arg, "~") || strings.Contains(arg, "/") ||
		strings.Contains(arg, "\\")
}

// NormalizeArgs reorders interleaved flags so flags appear before positional arguments.
// Arguments following a bare "--" are preserved in place without reordering.
func NormalizeArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}

	var flags []string
	var pos []string
	var afterDoubleDash []string
	hasDoubleDash := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if hasDoubleDash {
			afterDoubleDash = append(afterDoubleDash, arg)
			continue
		}
		if arg == "--" {
			hasDoubleDash = true
			continue
		}

		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			// Check if flag takes a separate value argument
			if !strings.Contains(arg, "=") && isValueFlag(arg) && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, arg)
		}
	}

	result := make([]string, 0, len(args))
	result = append(result, flags...)
	result = append(result, pos...)
	if hasDoubleDash {
		result = append(result, "--")
		result = append(result, afterDoubleDash...)
	}
	return result
}

// SanitizePath trims quotes, whitespace, and normalizes file paths (e.g. from drag & drop).
func SanitizePath(p string) string {
	cleaned := strings.TrimSpace(p)
	// Strip surrounding double or single quotes
	for (strings.HasPrefix(cleaned, "\"") && strings.HasSuffix(cleaned, "\"")) ||
		(strings.HasPrefix(cleaned, "'") && strings.HasSuffix(cleaned, "'")) {
		if len(cleaned) < 2 {
			break
		}
		cleaned = cleaned[1 : len(cleaned)-1]
		cleaned = strings.TrimSpace(cleaned)
	}

	if cleaned == "" {
		return ""
	}

	return filepath.Clean(cleaned)
}
