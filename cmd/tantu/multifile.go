package main

// Multi-file send: expanding a directory into a bounded, ordered list of
// individual file transfers.
//
// The design follows the recorded decision D-13 ("multi-select becomes
// multiple logical transfers"). A directory is not archived and not sent as
// one blob: each file becomes its own transfer with its own operation ID, its
// own idempotency key, and its own success or failure. That is what makes a
// partial failure honest - 40 of 50 files arriving is reported as 40 of 50,
// not as one failed transfer whose outcome nobody can reason about.
//
// It also means the receiver needs no new protocol and no new wire message.
// Every file travels the existing single-file path.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Bounds on a single directory expansion. Without them, `tantu send .` in a
// home directory or a node_modules tree would attempt thousands of transfers
// and hold them open against the receiver's concurrency budget, failing
// deep into the run with a quota error instead of at the point of the mistake.
const (
	// maxExpandedFiles bounds how many files one directory argument expands
	// to. It is generous for a real project and small enough that a runaway
	// walk fails immediately and visibly.
	maxExpandedFiles = 2000
	// maxExpandedTotalBytes bounds the aggregate payload. It matches the
	// receiver's own per-transfer ceiling, so a single expansion can never
	// promise more than one transfer's worth of data.
	maxExpandedTotalBytes int64 = 5 * 1024 * 1024 * 1024
)

// expandedFile is one file in a directory expansion: where it is on disk, and
// the name the peer should receive it under.
type expandedFile struct {
	// Path is the absolute-or-relative path to open locally.
	Path string
	// Name is the name the peer will see. Directory components are folded
	// into the name so two files with the same basename in different
	// subdirectories do not collide on the receiver.
	Name string
	// Size is the file's size in bytes at walk time.
	Size int64
}

// directoryExpansionError explains why a directory could not be expanded, in
// the same plain language as the rest of the send surface.
type directoryExpansionError struct {
	Code    string
	Message string
	// NextAction is the one safe thing the user can do next.
	NextAction string
}

func (e *directoryExpansionError) Error() string { return e.Message }

// expandDirectory walks root and returns the files it contains, in a stable
// order, with peer-facing names that cannot collide.
//
// Three properties matter and are each deliberate:
//
//   - Symlinks are not followed. A symlink to a parent directory would make
//     the walk non-terminating, and a symlink to somewhere outside the tree
//     would send files the user did not point at. Both are refused rather
//     than silently skipped, because a directory that silently omits files
//     looks exactly like a directory that fully sent.
//   - The order is sorted, so a run is reproducible and a failure names the
//     same file every time.
//   - Names are derived from the path relative to root, with separators
//     replaced, so `a/b/notes.txt` and `c/notes.txt` stay distinct on the
//     receiver's flat downloads folder.
func expandDirectory(root string) ([]expandedFile, *directoryExpansionError) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, &directoryExpansionError{
			Code:       "input_error",
			Message:    fmt.Sprintf("cannot read %s: %v", root, err),
			NextAction: "Check the path, or pass --text to send the string itself.",
		}
	}
	if !info.IsDir() {
		return nil, &directoryExpansionError{
			Code:       "invalid_input",
			Message:    fmt.Sprintf("%s is not a directory", root),
			NextAction: "Send a directory path, or a single file path.",
		}
	}

	var files []expandedFile
	var totalBytes int64
	var walkErr *directoryExpansionError

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if walkErr != nil {
			return filepath.SkipAll
		}
		if err != nil {
			// One unreadable entry must not abort the whole send silently, but
			// it also must not be invisible. It is recorded and reported.
			walkErr = &directoryExpansionError{
				Code:       "input_error",
				Message:    fmt.Sprintf("cannot read %s: %v", path, err),
				NextAction: "Check the permissions on that path, or send the files individually.",
			}
			return filepath.SkipAll
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			walkErr = &directoryExpansionError{
				Code:       "invalid_input",
				Message:    fmt.Sprintf("%s is a symlink", path),
				NextAction: "Send the real file directly. Symlinks are not followed, so a link cannot pull in files outside the directory.",
			}
			return filepath.SkipAll
		}
		if !d.Type().IsRegular() {
			// Sockets, devices and FIFOs have no meaningful content to send.
			return nil
		}
		fi, statErr := d.Info()
		if statErr != nil {
			walkErr = &directoryExpansionError{
				Code:       "input_error",
				Message:    fmt.Sprintf("cannot stat %s: %v", path, statErr),
				NextAction: "Check the permissions on that path, or send the files individually.",
			}
			return filepath.SkipAll
		}
		if len(files) >= maxExpandedFiles {
			walkErr = &directoryExpansionError{
				Code:       "limit_exceeded",
				Message:    fmt.Sprintf("%s contains more than %d files", root, maxExpandedFiles),
				NextAction: fmt.Sprintf("Send the files individually, or narrow the directory to the %d files you mean.", maxExpandedFiles),
			}
			return filepath.SkipAll
		}
		if totalBytes+fi.Size() > maxExpandedTotalBytes {
			walkErr = &directoryExpansionError{
				Code:       "limit_exceeded",
				Message:    fmt.Sprintf("%s contains more than the %d byte total limit", root, maxExpandedTotalBytes),
				NextAction: "Send a subset, or send a single archive instead.",
			}
			return filepath.SkipAll
		}
		totalBytes += fi.Size()
		files = append(files, expandedFile{
			Path: path,
			Name: flattenedTransferName(root, path),
			Size: fi.Size(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if err != nil && walkErr == nil {
		return nil, &directoryExpansionError{
			Code:       "input_error",
			Message:    fmt.Sprintf("cannot read %s: %v", root, err),
			NextAction: "Check the path, or send the files individually.",
		}
	}

	// A directory with nothing sendable in it is an honest error, not a
	// silent success: "sent 0 of 0 files" would read as a completed send.
	if len(files) == 0 {
		return nil, &directoryExpansionError{
			Code:       "invalid_input",
			Message:    fmt.Sprintf("%s contains no files to send", root),
			NextAction: "Check the path, or send a single file.",
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// flattenedTransferName derives a receiver-safe, collision-resistant name from
// a file's path relative to the expansion root.
//
// The receiver flattens every name to its basename, so a nested file sent as
// "notes.txt" would overwrite a top-level "notes.txt" - and worse, the second
// one would arrive as "notes (1).txt" with no indication of where it came
// from. Folding the relative path into the name keeps them distinct and
// readable: "docs-api-readme.md".
func flattenedTransferName(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	rel = filepath.ToSlash(rel)
	var b strings.Builder
	for _, r := range rel {
		switch {
		case r == '/':
			b.WriteRune('-')
		case r < 32 || r == 127:
			// Control characters have no business in a filename and are
			// stripped here rather than relying on the receiver.
		default:
			b.WriteRune(r)
		}
	}
	name := strings.Trim(b.String(), "-. ")
	if name == "" {
		name = "drop.bin"
	}
	// Bound the name to the same limit the receiver enforces, so a deep path
	// cannot produce a name the receiver would have to truncate (and thereby
	// make two distinct files identical).
	const maxName = 200
	if len(name) > maxName {
		ext := filepath.Ext(name)
		keep := maxName - len(ext)
		if keep < 8 {
			keep = maxName - 8
		}
		if ext != "" && len(ext) < maxName-8 {
			name = name[:keep] + ext
		} else {
			name = name[:maxName]
		}
	}
	return name
}
