// Command imgtool generates, mutates and verifies disk images for Gaia's
// Phase 1 spikes. It uses only the Go standard library on purpose: it must
// build anywhere, with no third-party code in the measurement path.
//
//	imgtool gen    -out FILE -kind logs|random -size 2GiB [-seed N]
//	imgtool mutate -file IMG -percent 1 [-block 64KiB] [-seed N]
//	imgtool hash   -file IMG
//	imgtool cmp    -a IMG -b IMG
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = cmdGen(os.Args[2:])
	case "mutate":
		err = cmdMutate(os.Args[2:])
	case "hash":
		err = cmdHash(os.Args[2:])
	case "cmp":
		err = cmdCmp(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "imgtool:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: imgtool gen|mutate|hash|cmp [flags]  (run a subcommand with -h for flags)")
	os.Exit(2)
}

// parseSize accepts plain bytes or KiB/MiB/GiB suffixes.
func parseSize(s string) (int64, error) {
	mult := int64(1)
	for suffix, m := range map[string]int64{"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30} {
		if strings.HasSuffix(s, suffix) {
			mult, s = m, strings.TrimSuffix(s, suffix)
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}

func emit(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// ---- gen ------------------------------------------------------------------

// cmdGen writes a file of a given kind. "logs" is compressible, repetitive
// text (like application logs); "random" is incompressible (like encrypted
// or media data). Deterministic for a given seed.
func cmdGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	out := fs.String("out", "", "output file")
	kind := fs.String("kind", "logs", "logs|random")
	sizeS := fs.String("size", "1GiB", "size (bytes, KiB, MiB, GiB)")
	seed := fs.Uint64("seed", 1, "random seed")
	_ = fs.Parse(args)
	if *out == "" {
		return errors.New("gen: -out is required")
	}
	size, err := parseSize(*sizeS)
	if err != nil {
		return err
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	rng := rand.New(rand.NewPCG(*seed, *seed^0x9e3779b97f4a7c15))
	start := time.Now()

	var written int64
	switch *kind {
	case "random":
		buf := make([]byte, 1<<20)
		for written < size {
			for i := 0; i+8 <= len(buf); i += 8 {
				v := rng.Uint64()
				for j := 0; j < 8; j++ {
					buf[i+j] = byte(v >> (8 * j))
				}
			}
			n := int64(len(buf))
			if size-written < n {
				n = size - written
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			written += n
		}
	case "logs":
		services := []string{"api", "auth", "billing", "worker", "scheduler", "gateway"}
		levels := []string{"INFO", "INFO", "INFO", "DEBUG", "WARN", "ERROR"}
		msgs := []string{
			"request completed", "cache miss", "retrying upstream call",
			"connection pool exhausted", "user session refreshed", "job finished",
		}
		ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		var line bytes.Buffer
		for written < size {
			ts = ts.Add(time.Duration(rng.IntN(500)) * time.Millisecond)
			line.Reset()
			fmt.Fprintf(&line, "%s %-5s %s req_id=%016x latency_ms=%d %s\n",
				ts.Format(time.RFC3339Nano), levels[rng.IntN(len(levels))],
				services[rng.IntN(len(services))], rng.Uint64(), rng.IntN(2000),
				msgs[rng.IntN(len(msgs))])
			b := line.Bytes()
			if int64(len(b)) > size-written {
				b = b[:size-written]
			}
			if _, err := w.Write(b); err != nil {
				return err
			}
			written += int64(len(b))
		}
	default:
		return fmt.Errorf("gen: unknown kind %q", *kind)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	emit(map[string]any{"op": "gen", "file": *out, "kind": *kind, "bytes": written,
		"seconds": time.Since(start).Seconds()})
	return nil
}

// ---- mutate ---------------------------------------------------------------

// cmdMutate overwrites randomly chosen, non-overlapping, block-aligned
// regions with random data until `percent` of the file has changed. This
// simulates the churn between two backups.
func cmdMutate(args []string) error {
	fs := flag.NewFlagSet("mutate", flag.ExitOnError)
	file := fs.String("file", "", "image to mutate in place")
	percent := fs.Float64("percent", 1, "percentage of the image to change")
	blockS := fs.String("block", "64KiB", "size of each changed region")
	seed := fs.Uint64("seed", 42, "random seed")
	_ = fs.Parse(args)
	if *file == "" {
		return errors.New("mutate: -file is required")
	}
	if *percent <= 0 || *percent > 100 {
		return errors.New("mutate: -percent must be in (0, 100]")
	}
	block, err := parseSize(*blockS)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*file, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	blocks := st.Size() / block
	if blocks == 0 {
		return errors.New("mutate: image smaller than one block")
	}
	want := int64(float64(blocks) * *percent / 100)
	if want == 0 {
		want = 1
	}
	rng := rand.New(rand.NewPCG(*seed, ^*seed))
	chosen := make(map[int64]struct{}, want)
	for int64(len(chosen)) < want {
		chosen[rng.Int64N(blocks)] = struct{}{}
	}
	// Write in offset order: map iteration order is random, and the data drawn from rng
	// for each block must not depend on it, or the same seed gives a different image.
	order := make([]int64, 0, len(chosen))
	for idx := range chosen {
		order = append(order, idx)
	}
	slices.Sort(order)
	buf := make([]byte, block)
	start := time.Now()
	for _, idx := range order {
		for i := range buf {
			buf[i] = byte(rng.Uint32())
		}
		if _, err := f.WriteAt(buf, idx*block); err != nil {
			return err
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	emit(map[string]any{"op": "mutate", "file": *file, "blocks_changed": want,
		"block_bytes": block, "bytes_changed": want * block,
		"percent_changed": float64(want*block) / float64(st.Size()) * 100,
		"seconds":         time.Since(start).Seconds()})
	return nil
}

// ---- hash / cmp -----------------------------------------------------------

type hashResult struct {
	File    string  `json:"file"`
	SHA256  string  `json:"sha256"`
	Bytes   int64   `json:"bytes"`
	Seconds float64 `json:"seconds"`
	MBps    float64 `json:"mb_per_s"`
}

func hashFile(path string) (hashResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return hashResult{}, err
	}
	defer f.Close()
	h := sha256.New()
	start := time.Now()
	n, err := io.CopyBuffer(h, f, make([]byte, 4<<20))
	if err != nil {
		return hashResult{}, err
	}
	sec := time.Since(start).Seconds()
	return hashResult{File: path, SHA256: hex.EncodeToString(h.Sum(nil)), Bytes: n,
		Seconds: sec, MBps: float64(n) / (1 << 20) / max(sec, 1e-9)}, nil
}

func cmdHash(args []string) error {
	fs := flag.NewFlagSet("hash", flag.ExitOnError)
	file := fs.String("file", "", "file to hash")
	_ = fs.Parse(args)
	if *file == "" {
		return errors.New("hash: -file is required")
	}
	r, err := hashFile(*file)
	if err != nil {
		return err
	}
	emit(r)
	return nil
}

// cmdCmp exits non-zero unless both files are byte-identical. It reports the
// first differing offset, which is what an operator needs when it fails.
func cmdCmp(args []string) error {
	fs := flag.NewFlagSet("cmp", flag.ExitOnError)
	a := fs.String("a", "", "first file")
	b := fs.String("b", "", "second file")
	_ = fs.Parse(args)
	if *a == "" || *b == "" {
		return errors.New("cmp: -a and -b are required")
	}
	fa, err := os.Open(*a)
	if err != nil {
		return err
	}
	defer fa.Close()
	fb, err := os.Open(*b)
	if err != nil {
		return err
	}
	defer fb.Close()
	ra, rb := bufio.NewReaderSize(fa, 4<<20), bufio.NewReaderSize(fb, 4<<20)
	bufA, bufB := make([]byte, 1<<20), make([]byte, 1<<20)
	var off int64
	for {
		na, errA := io.ReadFull(ra, bufA)
		nb, errB := io.ReadFull(rb, bufB)
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			n := min(na, nb)
			i := 0
			for i < n && bufA[i] == bufB[i] {
				i++
			}
			emit(map[string]any{"op": "cmp", "identical": false, "first_diff_offset": off + int64(i)})
			os.Exit(1)
		}
		off += int64(na)
		doneA := errA == io.EOF || errA == io.ErrUnexpectedEOF
		doneB := errB == io.EOF || errB == io.ErrUnexpectedEOF
		if errA != nil && !doneA {
			return errA
		}
		if errB != nil && !doneB {
			return errB
		}
		if doneA || doneB {
			break
		}
	}
	emit(map[string]any{"op": "cmp", "identical": true, "bytes": off})
	return nil
}
