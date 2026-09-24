package drop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxStagingManifestSize = 16 * 1024

var (
	// ErrStagingManifestMismatch means a partial file was previously created
	// for different transfer metadata. Resuming it would risk combining bytes
	// from unrelated content, so receivers fail closed instead.
	ErrStagingManifestMismatch = errors.New("staging manifest does not match drop metadata")
)

// StagingManifest is the durable identity of a resumable partial file. It is
// intentionally separate from the bytes: a filename and size alone are not
// enough to establish that a retry is the same transfer.
type StagingManifest struct {
	DropID    string    `json:"drop_id"`
	Kind      DropKind  `json:"kind"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	MIMEType  string    `json:"mime_type,omitempty"`
	ChunkSize int       `json:"chunk_size"`
	HeadHash  string    `json:"head_hash,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// StagingManifestPath returns the private sidecar path for a .part file.
func StagingManifestPath(partPath string) string {
	return partPath + ".meta"
}

func openManifestDirectory(partPath string) (*StagingDirectory, string, bool, error) {
	dirPath := filepath.Dir(partPath)
	if filepath.Base(dirPath) != StagingDirName {
		return nil, filepath.Base(partPath), false, nil
	}
	dir, err := OpenStagingDirectory(dirPath)
	if err != nil {
		return nil, "", true, err
	}
	return dir, filepath.Base(partPath), true, nil
}

func normalizedManifestMetadata(meta DropSend) DropSend {
	if meta.ChunkSize <= 0 {
		meta.ChunkSize = DefaultChunkSize
	}
	return meta
}

// NewStagingManifest creates a manifest for a validated drop metadata value.
func NewStagingManifest(meta DropSend) (StagingManifest, error) {
	if err := validateDropMetadata(meta); err != nil {
		return StagingManifest{}, err
	}
	meta = normalizedManifestMetadata(meta)
	return StagingManifest{
		DropID:    meta.DropID,
		Kind:      meta.Kind,
		Name:      meta.Name,
		Size:      meta.Size,
		MIMEType:  meta.MIMEType,
		ChunkSize: meta.ChunkSize,
		HeadHash:  meta.HeadHash,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// Matches reports whether a stored manifest describes the same transfer.
func (m StagingManifest) Matches(meta DropSend) bool {
	meta = normalizedManifestMetadata(meta)
	return m.DropID == meta.DropID &&
		m.Kind == meta.Kind &&
		m.Name == meta.Name &&
		m.Size == meta.Size &&
		m.MIMEType == meta.MIMEType &&
		m.ChunkSize == meta.ChunkSize &&
		strings.EqualFold(m.HeadHash, meta.HeadHash)
}

func createRootTemp(dir *StagingDirectory, base string) (*os.File, string, error) {
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf(".%s.tmp-%d", base, time.Now().UnixNano()+int64(i))
		file, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return file, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("could not allocate staging manifest temporary file")
}

// WriteStagingManifest atomically publishes a private manifest beside partPath.
// Private staging paths are opened through a verified os.Root, so replacing
// the staging pathname cannot redirect the manifest outside that directory.
func WriteStagingManifest(partPath string, meta DropSend) error {
	manifest, err := NewStagingManifest(meta)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal staging manifest: %w", err)
	}
	if len(data) > maxStagingManifestSize {
		return errors.New("staging manifest exceeds size limit")
	}
	dir, partName, rooted, err := openManifestDirectory(partPath)
	if err != nil {
		return err
	}
	if rooted {
		defer dir.Close()
	}
	dstName := partName + ".meta"
	dst := StagingManifestPath(partPath)
	var info os.FileInfo
	var statErr error
	if rooted {
		info, statErr = dir.Lstat(dstName)
	} else {
		info, statErr = os.Lstat(dst)
	}
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("staging manifest path is a symlink")
		}
		if !info.Mode().IsRegular() {
			return errors.New("staging manifest path is not a regular file")
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}

	var tmp *os.File
	var tmpName string
	if rooted {
		tmp, tmpName, err = createRootTemp(dir, dstName)
	} else {
		tmp, err = os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
		if err == nil {
			tmpName = tmp.Name()
		}
	}
	if err != nil {
		return fmt.Errorf("create staging manifest temp file: %w", err)
	}
	cleanup := func() {
		_ = tmp.Close()
		if rooted {
			_ = dir.Remove(tmpName)
		} else {
			_ = os.Remove(tmpName)
		}
	}
	if err := tmp.Chmod(0600); err != nil {
		cleanup()
		return fmt.Errorf("set staging manifest permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write staging manifest: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync staging manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close staging manifest: %w", err)
	}
	rename := func(oldName, newName string) error {
		if rooted {
			return dir.Rename(oldName, newName)
		}
		return os.Rename(oldName, dst)
	}
	removeDst := func() error {
		if rooted {
			return dir.Remove(dstName)
		}
		return os.Remove(dst)
	}
	if err := rename(tmpName, dstName); err != nil {
		// Windows does not replace an existing destination atomically. The
		// sidecar is recoverable (the partial is revalidated against the new
		// metadata on the next attempt), so remove-and-retry is acceptable.
		if removeErr := removeDst(); removeErr != nil && !os.IsNotExist(removeErr) {
			cleanup()
			return fmt.Errorf("replace staging manifest: %w", err)
		}
		if retryErr := rename(tmpName, dstName); retryErr != nil {
			cleanup()
			return fmt.Errorf("replace staging manifest: %w", retryErr)
		}
	}
	return nil
}

// ReadStagingManifest reads and bounds a sidecar manifest. A missing sidecar
// is reported as os.ErrNotExist; malformed or unsafe sidecars are hard errors.
func ReadStagingManifest(partPath string) (StagingManifest, error) {
	path := StagingManifestPath(partPath)
	dir, partName, rooted, err := openManifestDirectory(partPath)
	if err != nil {
		return StagingManifest{}, err
	}
	if rooted {
		defer dir.Close()
	}
	var info os.FileInfo
	if rooted {
		info, err = dir.Lstat(partName + ".meta")
	} else {
		info, err = os.Lstat(path)
	}
	if err != nil {
		return StagingManifest{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return StagingManifest{}, errors.New("staging manifest is not a regular file")
	}
	if info.Size() > maxStagingManifestSize {
		return StagingManifest{}, errors.New("staging manifest exceeds size limit")
	}
	var file *os.File
	if rooted {
		file, err = dir.OpenFile(partName+".meta", os.O_RDONLY, 0)
	} else {
		file, err = os.Open(path)
	}
	if err != nil {
		return StagingManifest{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxStagingManifestSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return StagingManifest{}, readErr
	}
	if closeErr != nil {
		return StagingManifest{}, closeErr
	}
	if len(data) > maxStagingManifestSize {
		return StagingManifest{}, errors.New("staging manifest exceeds size limit")
	}
	var manifest StagingManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return StagingManifest{}, fmt.Errorf("decode staging manifest: %w", err)
	}
	if manifest.DropID == "" || manifest.Kind == "" || manifest.ChunkSize <= 0 || manifest.ChunkSize > MaxChunkSize || manifest.CreatedAt.IsZero() {
		return StagingManifest{}, errors.New("staging manifest is incomplete")
	}
	if err := validateDropMetadata(DropSend{
		DropID:    manifest.DropID,
		Kind:      manifest.Kind,
		Name:      manifest.Name,
		Size:      manifest.Size,
		MIMEType:  manifest.MIMEType,
		ChunkSize: manifest.ChunkSize,
		HeadHash:  manifest.HeadHash,
	}); err != nil {
		return StagingManifest{}, fmt.Errorf("invalid staging manifest: %w", err)
	}
	return manifest, nil
}

// RemoveStagingManifest removes a sidecar if present. It is safe to call after
// a partial has already been published or swept.
func RemoveStagingManifest(partPath string) error {
	dir, partName, rooted, err := openManifestDirectory(partPath)
	if err != nil {
		return err
	}
	if rooted {
		defer dir.Close()
		err = dir.Remove(partName + ".meta")
	} else {
		err = os.Remove(StagingManifestPath(partPath))
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
