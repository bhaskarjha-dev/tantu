package hub

// Directory structure preservation: turning a sender-supplied relative path
// into a real place on disk, safely.
//
// `tantu send ./project/` used to flatten the tree into the downloads folder,
// folding each path into the filename (docs/api/readme.md became
// docs-api-readme.md). That was a deliberate narrowing under D-13, and it has
// one real cost users hit immediately: a project arrives as two hundred files
// with mangled names in one flat folder, and two files that differed only by
// directory became name soup. The README already called preserving structure
// "the natural next step", and the wire already had a field designed for
// exactly this kind of additive change.
//
// Everything here is about one rule: a relative path arrives from a peer, and
// a peer is untrusted. So the path is not sanitised into something usable - it
// is either provably safe or refused:
//
//   - no absolute paths, no drive letters, no UNC prefixes;
//   - no "..", no "." and no empty component, so the result cannot escape or
//     address its own parent;
//   - no control characters, no NTFS alternate-stream colon, no character
//     Win32 rejects;
//   - no trailing dot or space on any component, because Windows silently
//     strips them and "readme." and "readme" would become one file;
//   - no reserved DOS device name, so "aux" cannot become a device;
//   - bounded in length at both the whole path and each component, so a deep
//     path cannot produce a name the filesystem would truncate into a
//     collision.
//
// Every one of those is a rejection rather than a rewrite. A sanitiser that
// silently turns "../../etc/passwd" into "etc-passwd" produces a file the user
// did not ask for at a name they did not choose; a refusal produces an error
// they can act on.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Relative-path limits. They are deliberately below the smallest filesystem
// limits this ships on (255 bytes per component, 4096 for a whole path) so a
// refusal happens at acknowledgement rather than after the payload has already
// consumed the disk it was going to write.
const (
	// MaxRelPathLength bounds the whole slash-separated path.
	MaxRelPathLength = 1024
	// MaxRelPathComponent bounds one path component, leaving room for the
	// " (n)" collision suffix publication appends.
	MaxRelPathComponent = 200
	// MaxRelPathDepth bounds how deep a tree may be reconstructed. Deep enough
	// for any real project; shallow enough that a hostile peer cannot force a
	// thousand directories into someone's downloads folder.
	MaxRelPathDepth = 24
)

// reservedWindowsDeviceNames are the DOS device names Win32 resolves on any
// path, with or without an extension. A component matching one is refused
// rather than prefixed: "aux.txt" is not a file on Windows, and renaming it to
// "drop_aux.txt" would publish a name the sender never agreed to.
var reservedWindowsDeviceNames = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {},
	"COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {},
	"LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// RelPathError explains why a relative path was refused, in the product's
// plain language with one next action.
type RelPathError struct {
	Reason string
}

func (e *RelPathError) Error() string { return e.Reason }

// SanitizeRelPath validates a peer-supplied relative path and returns the
// components the receiver will create.
//
// The input is always slash-separated regardless of the receiver's platform: a
// sender on Windows and a receiver on Linux must agree on the same tree, and
// the sender is the only party that knows the sender's tree.
//
// An empty input is not an error and returns no components: that is the shape
// every single-file transfer has always had, and it must keep working
// unchanged. Structural preservation is strictly additive.
func SanitizeRelPath(rel string) ([]string, error) {
	// The emptiness test ignores surrounding whitespace, but the value that is
	// validated does not. Trimming first would silently rewrite "readme " into
	// "readme", and a path that had to be rewritten is a path the sender should
	// be told about - on Windows those two names are the same file, so the
	// rewrite would also merge two transfers the user meant to keep apart.
	if strings.TrimSpace(rel) == "" {
		return nil, nil
	}
	if len(rel) > MaxRelPathLength {
		return nil, &RelPathError{Reason: fmt.Sprintf("relative path exceeds %d bytes", MaxRelPathLength)}
	}
	// Backslashes are normalised rather than refused: a sender that walked a
	// Windows tree may produce either separator, and both mean the same path.
	normalized := strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(normalized, "/") {
		return nil, &RelPathError{Reason: "relative path must not be absolute"}
	}
	// "C:/x" and "//server/share" are absolute in the forms a Windows sender
	// can produce. Checking for the colon as well as the drive-letter shape
	// covers both, because a colon is refused in every component anyway.
	if len(normalized) >= 2 && normalized[1] == ':' {
		return nil, &RelPathError{Reason: "relative path must not name a drive"}
	}
	raw := strings.Split(normalized, "/")
	if len(raw) > MaxRelPathDepth {
		return nil, &RelPathError{Reason: fmt.Sprintf("relative path is deeper than %d levels", MaxRelPathDepth)}
	}
	components := make([]string, 0, len(raw))
	for _, segment := range raw {
		if segment == "" {
			return nil, &RelPathError{Reason: "relative path has an empty component"}
		}
		if segment == "." || segment == ".." {
			return nil, &RelPathError{Reason: "relative path must not contain . or .."}
		}
		for _, r := range segment {
			if r < 0x20 || r == 0x7f {
				return nil, &RelPathError{Reason: "relative path contains a control character"}
			}
			if r == ':' {
				// Covers both an NTFS alternate data stream ("file.txt:evil")
				// and any residual drive syntax.
				return nil, &RelPathError{Reason: "relative path must not contain ':'"}
			}
		}
		// Win32 rejects these outright; a transfer that failed only after the
		// payload was staged would be the worst place to discover it.
		if strings.ContainsAny(segment, "<>\"|?*") {
			return nil, &RelPathError{Reason: "relative path contains a character Windows does not allow in a name"}
		}
		// Windows strips trailing dots and spaces, so "readme" and "readme."
		// would publish to the same name. Refuse rather than pick one.
		if segment != strings.TrimRight(segment, ". ") {
			return nil, &RelPathError{Reason: "relative path component ends with a dot or space"}
		}
		if strings.TrimSpace(segment) != segment {
			return nil, &RelPathError{Reason: "relative path component begins or ends with whitespace"}
		}
		if len(segment) > MaxRelPathComponent {
			return nil, &RelPathError{Reason: fmt.Sprintf("relative path component exceeds %d bytes", MaxRelPathComponent)}
		}
		base := segment
		if dot := strings.Index(segment, "."); dot >= 0 {
			base = segment[:dot]
		}
		if _, reserved := reservedWindowsDeviceNames[strings.ToUpper(base)]; reserved {
			return nil, &RelPathError{Reason: "relative path component is a reserved device name"}
		}
		components = append(components, segment)
	}
	return components, nil
}

// JoinRelComponents turns validated components into a path relative to the
// receiver's output directory. It is the only place that converts a peer
// string into a filesystem path, so it is the only place that has to be right.
func JoinRelComponents(components []string) string {
	if len(components) == 0 {
		return ""
	}
	return filepath.Join(components...)
}

// relRoot is the anchored-handle surface EnsureRelDirs needs. It is satisfied
// by *drop.StagingDirectory, which is the only implementation that exists, and
// naming it as an interface keeps this function testable without a real
// filesystem and makes the required confinement explicit at the call site.
type relRoot interface {
	Lstat(name string) (os.FileInfo, error)
	Mkdir(name string, perm os.FileMode) error
}

// EnsureRelDirs creates the parent directories of a relative destination
// inside root, refusing to traverse a symlink at any level.
//
// The refusal is the point. Creating directories is the first time a
// peer-chosen string becomes filesystem state, and a peer that could get a
// symlink accepted here would have every later file in that branch written
// wherever the link points - outside the output directory, with no error and
// no trace. Because the components were already refused for "..", ".", empty
// segments and absolute prefixes, nothing below can leave the root; the check
// on each component is what stops an already-planted link from redirecting a
// write that is otherwise perfectly anchored.
//
// Creating a directory the transfer then fails to publish is a minor cost: an
// empty folder under the downloads directory is recoverable by hand, whereas a
// file written outside it is not.
func EnsureRelDirs(root relRoot, rel string) error {
	if root == nil || rel == "" {
		return nil
	}
	sep := string(filepath.Separator)
	parts := strings.Split(rel, sep)
	// The last component is the file itself.
	dirs := parts[:len(parts)-1]
	if len(dirs) == 0 {
		return nil
	}
	accumulated := ""
	for _, part := range dirs {
		if accumulated == "" {
			accumulated = part
		} else {
			accumulated += sep + part
		}
		info, err := root.Lstat(accumulated)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s is a symlink; refusing to publish through it", accumulated)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s exists and is not a directory", accumulated)
			}
			continue
		case !os.IsNotExist(err):
			return fmt.Errorf("inspect %s: %w", accumulated, err)
		}
		if err := root.Mkdir(accumulated, 0o700); err != nil {
			if !os.IsExist(err) {
				return fmt.Errorf("create %s: %w", accumulated, err)
			}
			// Someone (a concurrent transfer, or the user) created it between
			// the Lstat and the Mkdir. Re-inspect: "exists" is only acceptable
			// when it is a real directory.
			info, statErr := root.Lstat(accumulated)
			if statErr != nil {
				return fmt.Errorf("inspect %s: %w", accumulated, statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("%s exists and is not a real directory", accumulated)
			}
		}
	}
	return nil
}
