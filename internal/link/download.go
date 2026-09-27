package link

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrExists means Download's destination already exists and overwrite wasn't
// set. Download returns the destination path along with it.
var ErrExists = errors.New("the destination file already exists")

// Download copies a file from the printer's storage to dest on this computer.
// dest may be a folder (the file keeps its long name from the printer), a file
// path, or empty for the current folder. An existing file is only replaced
// with overwrite. It returns the local path and the number of bytes written.
func (c *Client) Download(ctx context.Context, printerPath, dest string, overwrite bool) (string, int64, error) {
	meta, err := FilePath(printerPath)
	if err != nil {
		return "", 0, err
	}
	var info struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Type        string `json:"type"`
		Refs        struct {
			Download string `json:"download"`
		} `json:"refs"`
	}
	if _, err := c.Get(ctx, meta, &info); err != nil {
		return "", 0, err
	}
	if info.Refs.Download == "" {
		return "", 0, fmt.Errorf("%s is not a downloadable file (type %s)", printerPath, info.Type)
	}
	src, err := downloadPath(info.Refs.Download)
	if err != nil {
		return "", 0, err
	}

	name := info.DisplayName
	if name == "" {
		name = info.Name
	}
	if name = filepath.Base(name); name == "." || name == "/" || name == ".." {
		return "", 0, fmt.Errorf("the printer reported an unusable file name %q", info.DisplayName)
	}
	target := dest
	if target == "" {
		target = "."
	}
	if st, err := os.Stat(target); err == nil && st.IsDir() {
		target = filepath.Join(target, name)
	}
	if !overwrite {
		if _, err := os.Stat(target); err == nil {
			return target, 0, ErrExists
		}
	}

	resp, err := c.Do(ctx, Request{
		Method:  http.MethodGet,
		Path:    src,
		Header:  http.Header{"Accept": {"*/*"}},
		Timeout: 30 * time.Minute,
	})
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	// Write next to the target and rename, so a failed transfer never
	// leaves a truncated print file behind.
	tmp, err := os.CreateTemp(filepath.Dir(target), ".prusactl-download-*")
	if err != nil {
		return "", 0, err
	}
	n, err := io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && resp.ContentLength >= 0 && n != resp.ContentLength {
		err = fmt.Errorf("the printer sent %d of %d bytes", n, resp.ContentLength)
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), target)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", 0, fmt.Errorf("downloading %s: %w", printerPath, err)
	}
	return target, n, nil
}

// downloadPath escapes the printer's download reference (e.g.
// "/usb/PART~1.BGC") segment by segment, refusing anything outside it.
func downloadPath(ref string) (string, error) {
	if !strings.HasPrefix(ref, "/") || strings.Contains(ref, "://") {
		return "", fmt.Errorf("unexpected download reference %q", ref)
	}
	parts := strings.Split(strings.TrimPrefix(ref, "/"), "/")
	for i, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("unexpected download reference %q", ref)
		}
		parts[i] = url.PathEscape(p)
	}
	return "/" + strings.Join(parts, "/"), nil
}
