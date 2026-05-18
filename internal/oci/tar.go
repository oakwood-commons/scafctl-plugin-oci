package oci

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// writeTarDir writes the contents of a directory as a tar archive to w.
func writeTarDir(w io.Writer, dir string) error {
	// Resolve to absolute to prevent path traversal.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving directory path: %w", err)
	}

	tw := tar.NewWriter(w)

	walkErr := filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip symlinks to avoid tar issues and path traversal.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		rel, err := filepath.Rel(absDir, path)
		if err != nil {
			return err
		}

		// Skip the root directory itself.
		if rel == "." {
			return nil
		}

		// Skip non-regular files (FIFOs, device nodes, sockets, etc.).
		// Check before writing header to avoid orphaned entries in the tar.
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		return copyFileToTar(tw, path)
	})

	// Close the tar writer first — this flushes final padding.
	if closeErr := tw.Close(); closeErr != nil && walkErr == nil {
		return closeErr
	}
	return walkErr
}

// copyFileToTar copies a file's contents into the tar writer.
func copyFileToTar(tw *tar.Writer, path string) error {
	f, err := os.Open(path) //nolint:gosec // path is resolved from filepath.Walk within a validated directory
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(tw, f)

	if closeErr := f.Close(); closeErr != nil && copyErr == nil {
		return closeErr
	}
	return copyErr
}
