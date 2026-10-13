package installation

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func fileHash(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func packExport(stage, output string) (err error) {
	f, err := os.CreateTemp(filepath.Dir(output), ".dispatch-export-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	z := gzip.NewWriter(f)
	w := tar.NewWriter(z)
	for _, name := range []string{"manifest.json", "dispatch.env", "data.tar.gz"} {
		src, e := os.Open(filepath.Join(stage, name))
		if e != nil {
			return e
		}
		info, e := src.Stat()
		if e == nil {
			e = w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: info.Size(), Typeflag: tar.TypeReg})
		}
		if e == nil {
			_, e = io.Copy(w, src)
		}
		src.Close()
		if e != nil {
			return e
		}
	}
	if err = w.Close(); err != nil {
		return err
	}
	if err = z.Close(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return os.Link(f.Name(), output)
}

func unpackExport(input, stage string) (exportManifest, error) {
	var meta exportManifest
	f, err := os.Open(input)
	if err != nil {
		return meta, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return meta, err
	}
	defer z.Close()
	r := tar.NewReader(z)
	seen := map[string]bool{}
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return meta, e
		}
		if seen[h.Name] || (h.Name != "manifest.json" && h.Name != "dispatch.env" && h.Name != "data.tar.gz") || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > 100<<30 || (h.Name != "data.tar.gz" && h.Size > 1<<20) {
			return meta, errors.New("invalid export archive entry")
		}
		seen[h.Name] = true
		dst, e := os.OpenFile(filepath.Join(stage, h.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return meta, e
		}
		_, e = io.Copy(dst, r)
		ce := dst.Close()
		if e != nil {
			return meta, e
		}
		if ce != nil {
			return meta, ce
		}
	}
	// Read through gzip's trailer so corruption cannot be hidden after tar's end.
	if err = finishArchive(z); err != nil {
		return meta, err
	}
	if len(seen) != 3 {
		return meta, errors.New("export archive is incomplete")
	}
	raw, err := os.ReadFile(filepath.Join(stage, "manifest.json"))
	if err != nil {
		return meta, err
	}
	if err = json.Unmarshal(raw, &meta); err != nil {
		return meta, err
	}
	if (meta.Format != 1 && meta.Format != 2) || meta.Format == 1 && len(meta.Mounts) > 0 || meta.Format == 2 && len(meta.Mounts) == 0 || !imagePattern.MatchString(meta.Image) || len(meta.Files) != 2 {
		return meta, errors.New("unsupported or invalid export manifest")
	}
	if err = validateDataMounts(meta.Mounts); err != nil {
		return meta, err
	}
	for _, name := range []string{"dispatch.env", "data.tar.gz"} {
		sum, e := fileHash(filepath.Join(stage, name))
		if e != nil {
			return meta, e
		}
		if sum != meta.Files[name] {
			return meta, errors.New("export checksum does not match")
		}
	}
	return meta, nil
}

// Validate every path and link before Docker's tar sees the archive. A second,
// normalized archive fixes ownership and drops special permission bits.
func validateData(input, output string) (err error) {
	f, err := os.Open(input)
	if err != nil {
		return err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer z.Close()
	r := tar.NewReader(z)
	var dst *os.File
	var zw *gzip.Writer
	var w *tar.Writer
	if output != "" {
		dst, err = os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer dst.Close()
		zw = gzip.NewWriter(dst)
		w = tar.NewWriter(zw)
	}
	entries := map[string]byte{}
	links := map[string]string{}
	var total int64
	count := 0
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		count++
		if count > 1000000 {
			return errors.New("data archive exceeds one million entries")
		}
		name := strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")
		if name == "." || name == "" {
			if h.Typeflag != tar.TypeDir {
				return errors.New("invalid data root")
			}
			continue
		}
		if path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00") {
			return fmt.Errorf("unsafe data path %q", name)
		}
		if name == "dispatch-master.key" && h.Size > 4096 {
			return errors.New("vault key exceeds 4 KiB")
		}
		if _, ok := entries[name]; ok {
			return errors.New("duplicate data path")
		}
		entries[name] = h.Typeflag
		if h.Size < 0 || h.Size > 100<<30-total {
			return errors.New("data archive exceeds 100 GiB or one million entries")
		}
		total += h.Size
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeDir:
		case tar.TypeSymlink:
			target := path.Clean(path.Join(path.Dir(name), h.Linkname))
			if path.IsAbs(h.Linkname) || target == ".." || strings.HasPrefix(target, "../") || strings.Contains(h.Linkname, "\\") {
				return errors.New("unsafe data symlink")
			}
			links[name] = target
			h.Linkname, e = filepath.Rel(path.Dir(name), target)
			if e != nil {
				return e
			}
		case tar.TypeLink:
			target := strings.TrimPrefix(h.Linkname, "./")
			if path.IsAbs(target) || path.Clean(target) != target || strings.HasPrefix(target, "../") || target == ".." {
				return errors.New("unsafe data hard link")
			}
			links[name] = target
			h.Linkname = target
		default:
			return errors.New("unsupported data archive entry")
		}
		if w != nil {
			h.Name = name
			h.Uid = 65532
			h.Gid = 65532
			h.Uname = ""
			h.Gname = ""
			h.Mode &= 0777
			h.PAXRecords = nil
			h.Xattrs = nil
			if e = w.WriteHeader(h); e != nil {
				return e
			}
			if _, e = io.Copy(w, r); e != nil {
				return e
			}
		}
	}
	if err = finishArchive(z); err != nil {
		return err
	}
	for name := range entries {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if kind, ok := entries[parent]; ok && kind != tar.TypeDir {
				return errors.New("data path has a non-directory parent")
			}
		}
	}
	for name, target := range links {
		kind, exists := entries[target]
		if entries[name] == tar.TypeLink && (!exists || (kind != tar.TypeReg && kind != tar.TypeRegA)) {
			return errors.New("data hard link must target a regular file")
		}
	}
	for _, name := range []string{"dispatch.db", "dispatch-master.key"} {
		kind, exists := entries[name]
		if !exists || (kind != tar.TypeReg && kind != tar.TypeRegA) {
			return errors.New("data archive is missing its database or vault key")
		}
	}
	if w != nil {
		if err = w.Close(); err != nil {
			return err
		}
		if err = zw.Close(); err != nil {
			return err
		}
		return dst.Sync()
	}
	return nil
}

// GNU tar pads its last record. Bound padding while still reading through the
// gzip trailer, which verifies the compressed stream's checksum.
func finishArchive(r io.Reader) error {
	const limit = 1 << 20
	n, err := io.Copy(io.Discard, io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errors.New("export archive has excessive trailing data")
	}
	return nil
}
