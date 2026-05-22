package oci

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// writeTarDir writes the contents of a directory as a tar archive to w.
// When layerRoot is non-empty, all paths are prefixed with it inside the tar.
func writeTarDir(w io.Writer, dir, layerRoot string) error {
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
		name := filepath.ToSlash(rel)
		if layerRoot != "" {
			name = strings.TrimLeft(filepath.ToSlash(filepath.Join(layerRoot, rel)), "/")
		}
		header.Name = name

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

// isTarFile returns true if the file appears to be a tar or tar.gz archive
// based on its extension.
func isTarFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".tar") ||
		strings.HasSuffix(lower, ".tar.gz") ||
		strings.HasSuffix(lower, ".tgz")
}

// writeSingleFileTar writes a single file as a tar entry with the given
// in-container destination name.
func writeSingleFileTar(w io.Writer, srcPath, destName string, fi os.FileInfo) error {
	tw := tar.NewWriter(w)

	header := &tar.Header{
		Name: strings.TrimLeft(filepath.ToSlash(destName), "/"),
		Size: fi.Size(),
		Mode: int64(fi.Mode().Perm()),
	}

	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("writing tar header: %w", err)
	}

	if err := copyFileToTar(tw, srcPath); err != nil {
		return fmt.Errorf("copying file to tar: %w", err)
	}

	return tw.Close()
}

// rewriteTarPrefix reads a tar (or tar.gz) file and rewrites all entry paths
// by prepending layerRoot.
func rewriteTarPrefix(w io.Writer, srcPath, layerRoot string) error {
	f, err := os.Open(srcPath) //nolint:gosec // path validated by caller
	if err != nil {
		return fmt.Errorf("opening tar %q: %w", srcPath, err)
	}
	defer f.Close() //nolint:errcheck // best-effort close on read-only file

	var reader io.Reader = f
	if strings.HasSuffix(strings.ToLower(srcPath), ".gz") || strings.HasSuffix(strings.ToLower(srcPath), ".tgz") {
		gz, gzErr := gzip.NewReader(f)
		if gzErr != nil {
			return fmt.Errorf("decompressing %q: %w", srcPath, gzErr)
		}
		defer gz.Close() //nolint:errcheck // best-effort close on read-only decompressor
		reader = gz
	}

	tr := tar.NewReader(reader)
	tw := tar.NewWriter(w)

	for {
		header, readErr := tr.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("reading tar entry: %w", readErr)
		}

		// Sanitize the entry name: strip leading slashes and clean traversal segments
		// so all rewritten entries remain under layerRoot.
		cleanName := path.Clean(strings.TrimLeft(header.Name, "/"))
		if cleanName == "." || cleanName == ".." || strings.HasPrefix(cleanName, "../") {
			continue
		}
		joined := filepath.Join(layerRoot, cleanName)
		header.Name = strings.TrimLeft(filepath.ToSlash(joined), "/")

		// Sanitize symlink/hardlink targets to prevent escaping layerRoot.
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			cleanLink := path.Clean(strings.TrimLeft(header.Linkname, "/"))
			if cleanLink == "." || cleanLink == ".." || strings.HasPrefix(cleanLink, "../") {
				continue
			}
			if header.Typeflag == tar.TypeLink {
				// Hard links are absolute within the tar — rewrite under layerRoot.
				linkJoined := filepath.Join(layerRoot, cleanLink)
				header.Linkname = strings.TrimLeft(filepath.ToSlash(linkJoined), "/")
			} else {
				// Symlinks are relative — reject targets that escape via "..".
				if strings.HasPrefix(cleanLink, "..") {
					continue
				}
				header.Linkname = cleanLink
			}
		}

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("writing rewritten header: %w", err)
		}

		if header.Typeflag == tar.TypeReg || header.Typeflag == 0 {
			if _, err := io.Copy(tw, io.LimitReader(tr, header.Size)); err != nil { //nolint:gosec // size from trusted tar header, not user input
				return fmt.Errorf("copying tar entry: %w", err)
			}
		}
	}

	return tw.Close()
}

// totalImageSize returns the total size of an image (config + all compressed layers).
func totalImageSize(img v1.Image) (int64, error) {
	cfgBytes, err := img.RawConfigFile()
	if err != nil {
		return 0, fmt.Errorf("reading config: %w", err)
	}
	total := int64(len(cfgBytes))

	layers, err := img.Layers()
	if err != nil {
		return 0, fmt.Errorf("reading layers: %w", err)
	}
	for _, l := range layers {
		size, sizeErr := l.Size()
		if sizeErr != nil {
			return 0, fmt.Errorf("reading layer size: %w", sizeErr)
		}
		total += size
	}
	return total, nil
}
