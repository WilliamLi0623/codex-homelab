package sessionruntime

import (
	"archive/tar"
	"errors"
	"io"
	"path"
	"strings"
)

const (
	bootstrapArchivePolicyTLS    bootstrapArchivePolicy = iota + 1
	bootstrapArchiveMaxBytes     int64                  = 8 << 30
	bootstrapArchiveMaxEntries                          = 100000
	bootstrapArchiveMaxPathBytes                        = 4096
)

type bootstrapArchivePolicy uint8

type bootstrapArchiveLimits struct {
	maxBytes     int64
	maxEntries   int
	maxPathBytes int
}

var errBootstrapArchiveInvalid = errors.New("invalid sanitation archive")

func validateBootstrapSanitationArchive(input io.Reader, policy bootstrapArchivePolicy) error {
	return validateBootstrapSanitationArchiveWithLimits(input, policy, bootstrapArchiveLimits{
		maxBytes:     bootstrapArchiveMaxBytes,
		maxEntries:   bootstrapArchiveMaxEntries,
		maxPathBytes: bootstrapArchiveMaxPathBytes,
	})
}

func validateBootstrapSanitationArchiveWithLimits(input io.Reader, policy bootstrapArchivePolicy, limits bootstrapArchiveLimits) error {
	roots, ok := bootstrapArchivePolicyRoots(policy)
	if !ok || input == nil || limits.maxBytes <= 0 || limits.maxEntries <= 0 || limits.maxPathBytes <= 0 {
		return errBootstrapArchiveInvalid
	}
	counted := &bootstrapArchiveCountingReader{reader: input, maxBytes: limits.maxBytes}
	archive := tar.NewReader(counted)
	seen := make(map[string]struct{})
	covered := make([]bool, len(roots))
	entries := 0

	for {
		beforeHeader := counted.read
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			if !counted.hasTerminatingZeroBlocksSince(beforeHeader) {
				return errBootstrapArchiveInvalid
			}
			break
		}
		if err != nil {
			return errBootstrapArchiveInvalid
		}
		entries++
		if entries > limits.maxEntries {
			return errBootstrapArchiveInvalid
		}
		name, ok := cleanBootstrapArchiveName(header.Name, limits.maxPathBytes)
		if !ok {
			return errBootstrapArchiveInvalid
		}
		if _, duplicate := seen[name]; duplicate {
			return errBootstrapArchiveInvalid
		}
		seen[name] = struct{}{}

		rootIndex := bootstrapArchiveRootIndex(name, roots)
		if rootIndex < 0 {
			return errBootstrapArchiveInvalid
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return errBootstrapArchiveInvalid
		}
		covered[rootIndex] = true
		if _, err := io.Copy(io.Discard, archive); err != nil {
			return errBootstrapArchiveInvalid
		}
	}

	for _, present := range covered {
		if !present {
			return errBootstrapArchiveInvalid
		}
	}
	if !bootstrapArchiveOnlyZeroPadding(counted) {
		return errBootstrapArchiveInvalid
	}
	return nil
}

func bootstrapArchivePolicyRoots(policy bootstrapArchivePolicy) ([]string, bool) {
	switch policy {
	case bootstrapArchivePolicyTLS:
		return []string{
			"etc/ssl/private/ssl-cert-snakeoil.key",
			"etc/ssl/certs/ssl-cert-snakeoil.pem",
		}, true
	default:
		return nil, false
	}
}

type bootstrapArchiveCountingReader struct {
	reader   io.Reader
	read     int64
	maxBytes int64
	tail     [1024]byte
	tailNext int
	tailLen  int
}

func (r *bootstrapArchiveCountingReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	remaining := r.maxBytes - r.read
	if remaining <= 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 || err == nil {
			return 0, errBootstrapArchiveInvalid
		}
		return 0, err
	}
	if int64(len(p)) > remaining {
		p = p[:int(remaining)]
	}
	n, err := r.reader.Read(p)
	r.read += int64(n)
	for _, value := range p[:n] {
		r.tail[r.tailNext] = value
		r.tailNext = (r.tailNext + 1) % len(r.tail)
		if r.tailLen < len(r.tail) {
			r.tailLen++
		}
	}
	return n, err
}

func (r *bootstrapArchiveCountingReader) hasTerminatingZeroBlocksSince(start int64) bool {
	consumed := r.read - start
	if consumed < int64(len(r.tail)) || consumed > int64(len(r.tail)+511) || r.tailLen != len(r.tail) {
		return false
	}
	for offset := range r.tail {
		if r.tail[(r.tailNext+offset)%len(r.tail)] != 0 {
			return false
		}
	}
	return true
}

func cleanBootstrapArchiveName(name string, maxBytes int) (string, bool) {
	if name == "" || strings.HasSuffix(name, "/") || len(name) > maxBytes || strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) {
		return "", false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." {
			return "", false
		}
	}
	clean := path.Clean(name)
	return clean, clean == name
}

func bootstrapArchiveRootIndex(name string, roots []string) int {
	for index, root := range roots {
		if name == root {
			return index
		}
	}
	return -1
}

func bootstrapArchiveOnlyZeroPadding(input io.Reader) bool {
	var buffer [32 * 1024]byte
	var total int64
	for {
		n, err := input.Read(buffer[:])
		for _, value := range buffer[:n] {
			if value != 0 {
				return false
			}
		}
		total += int64(n)
		if err != nil {
			return errors.Is(err, io.EOF) && total%512 == 0
		}
		if n == 0 {
			return false
		}
	}
}
