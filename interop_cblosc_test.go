package blosc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Optional compatibility tests against a system install of c-blosc 1.x.
// They are skipped when pkg-config cannot find blosc and libblosc is not
// on the usual library paths. Blosc2 (libblosc2) is not used.

const interopSource = `#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <blosc.h>

static unsigned char *read_all(size_t *n) {
    size_t cap = 4096, len = 0;
    unsigned char *buf = malloc(cap);
    if (!buf) return NULL;
    for (;;) {
        if (len == cap) {
            cap *= 2;
            unsigned char *grown = realloc(buf, cap);
            if (!grown) { free(buf); return NULL; }
            buf = grown;
        }
        size_t got = fread(buf + len, 1, cap - len, stdin);
        len += got;
        if (got == 0) break;
    }
    if (ferror(stdin)) { free(buf); return NULL; }
    *n = len;
    return buf;
}

int main(int argc, char **argv) {
    size_t nin = 0;
    unsigned char *in = read_all(&nin);
    if (!in || argc < 2) return 2;
    if (strcmp(argv[1], "compress") == 0) {
        if (argc < 6) return 2;
        int level = atoi(argv[2]);
        int shuffle = atoi(argv[3]);
        size_t typesize = (size_t)atoi(argv[4]);
        const char *codec = argv[5];
        size_t destsize = nin + BLOSC_MAX_OVERHEAD;
        void *dest = malloc(destsize ? destsize : 1);
        if (!dest) return 2;
        size_t blocksize = 0;
        if (argc >= 7) blocksize = (size_t)atoi(argv[6]);
        int n = blosc_compress_ctx(level, shuffle, typesize, nin, in, dest,
                                   destsize, codec, blocksize, 1);
        if (n <= 0) return 1;
        if (fwrite(dest, 1, (size_t)n, stdout) != (size_t)n) return 2;
        return 0;
    }
    if (strcmp(argv[1], "decompress") == 0) {
        size_t nbytes = 0, cbytes = 0, blocksize = 0;
        blosc_cbuffer_sizes(in, &nbytes, &cbytes, &blocksize);
        void *dest = malloc(nbytes ? nbytes : 1);
        if (!dest) return 2;
        int n = blosc_decompress_ctx(in, dest, nbytes, 1);
        if (n < 0) return 1;
        if ((size_t)n != nbytes) return 1;
        if (nbytes > 0 && fwrite(dest, 1, nbytes, stdout) != nbytes) return 2;
        return 0;
    }
    return 2;
}
`

var (
	interopOnce sync.Once
	interopBin  string
	interopEnv  []string
	interopSkip string
	interopFail error
)

func cbloscTool(t *testing.T) string {
	t.Helper()
	interopOnce.Do(prepareInterop)
	if interopSkip != "" {
		t.Skip(interopSkip)
	}
	if interopFail != nil {
		t.Fatal(interopFail)
	}
	return interopBin
}

func prepareInterop() {
	cflags, libs, libDir, ok := findBlosc()
	if interopFail != nil {
		return
	}
	if !ok {
		interopSkip = "c-blosc is not installed (pkg-config blosc or libblosc)"
		return
	}
	cc, err := findCC()
	if err != nil {
		interopFail = err
		return
	}
	dir, err := os.MkdirTemp("", "go-blosc-interop-")
	if err != nil {
		interopFail = err
		return
	}
	src := filepath.Join(dir, "interop.c")
	if err := os.WriteFile(src, []byte(interopSource), 0o644); err != nil {
		interopFail = err
		return
	}
	bin := filepath.Join(dir, "interop")
	args := []string{src, "-O2", "-o", bin}
	if cflags != "" {
		args = append(args, strings.Fields(cflags)...)
	}
	args = append(args, strings.Fields(libs)...)
	cmd := exec.Command(cc, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		interopFail = fmt.Errorf("compile c-blosc interop tool: %w\n%s", err, out)
		return
	}
	interopBin = bin
	interopEnv = envWithoutBlosc()
	if libDir != "" {
		interopEnv = append(interopEnv,
			"DYLD_LIBRARY_PATH="+libDir,
			"LD_LIBRARY_PATH="+libDir,
		)
	}
}

func findCC() (string, error) {
	for _, name := range []string{"cc", "gcc", "clang"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("no C compiler (cc, gcc, or clang) found to build the c-blosc interop tool")
}

func findBlosc() (cflags, libs, libDir string, ok bool) {
	if _, err := exec.LookPath("pkg-config"); err == nil {
		if exec.Command("pkg-config", "--exists", "blosc").Run() == nil {
			c, err1 := exec.Command("pkg-config", "--cflags", "blosc").Output()
			l, err2 := exec.Command("pkg-config", "--libs", "blosc").Output()
			if err1 == nil && err2 == nil {
				return strings.TrimSpace(string(c)), strings.TrimSpace(string(l)), libDirFromFlags(string(l)), true
			}
		}
	}
	candidates := []string{
		"/opt/homebrew/lib/libblosc.dylib",
		"/usr/local/lib/libblosc.dylib",
		"/usr/lib/libblosc.dylib",
		"/usr/lib/x86_64-linux-gnu/libblosc.so",
		"/usr/lib/aarch64-linux-gnu/libblosc.so",
		"/usr/local/lib/libblosc.so",
		"/usr/lib/libblosc.so",
	}
	for _, lib := range candidates {
		if _, err := os.Stat(lib); err != nil {
			continue
		}
		dir := filepath.Dir(lib)
		inc := filepath.Join(filepath.Dir(dir), "include")
		return "-I" + inc, "-L" + dir + " -lblosc", dir, true
	}
	if cflags, libs, libDir, ok := buildSiblingBlosc(); ok {
		return cflags, libs, libDir, true
	}
	return "", "", "", false
}

// buildSiblingBlosc compiles ../c-blosc into a cached prefix when the system
// library is absent. Zarr v3's Blosc codec is that library, including snappy,
// which upstream CMake leaves off by default.
func buildSiblingBlosc() (cflags, libs, libDir string, ok bool) {
	src := filepath.Join("..", "c-blosc")
	if _, err := os.Stat(filepath.Join(src, "CMakeLists.txt")); err != nil {
		return "", "", "", false
	}
	if _, err := exec.LookPath("cmake"); err != nil {
		interopFail = errors.New("cmake is required to build ../c-blosc for the interop tests")
		return "", "", "", false
	}
	prefix := filepath.Join(os.TempDir(), "go-blosc-cblosc-prefix")
	lib := filepath.Join(prefix, "lib", "libblosc.dylib")
	if _, err := os.Stat(lib); err != nil {
		lib = filepath.Join(prefix, "lib", "libblosc.so")
	}
	if _, err := os.Stat(lib); err != nil {
		build := filepath.Join(prefix, "build")
		cmakeArgs := []string{"-S", src, "-B", build,
			"-DCMAKE_BUILD_TYPE=Release",
			"-DCMAKE_INSTALL_PREFIX=" + prefix,
			"-DBUILD_TESTS=OFF",
			"-DBUILD_BENCHMARKS=OFF",
			"-DBUILD_FUZZERS=OFF",
			"-DDEACTIVATE_SNAPPY=OFF",
		}
		if fileExists("/opt/homebrew/include/snappy-c.h") {
			cmakeArgs = append(cmakeArgs, "-DCMAKE_PREFIX_PATH=/opt/homebrew")
		}
		cfg := exec.Command("cmake", cmakeArgs...)
		if out, err := cfg.CombinedOutput(); err != nil {
			interopFail = fmt.Errorf("configure ../c-blosc: %w\n%s", err, out)
			return "", "", "", false
		}
		if out, err := exec.Command("cmake", "--build", build, "--parallel").CombinedOutput(); err != nil {
			interopFail = fmt.Errorf("build ../c-blosc: %w\n%s", err, out)
			return "", "", "", false
		}
		if out, err := exec.Command("cmake", "--install", build).CombinedOutput(); err != nil {
			interopFail = fmt.Errorf("install ../c-blosc: %w\n%s", err, out)
			return "", "", "", false
		}
	}
	inc := filepath.Join(prefix, "include")
	dir := filepath.Join(prefix, "lib")
	if staticLib := filepath.Join(dir, "libblosc.a"); fileExists(staticLib) {
		libs := staticLib
		var snappyDir string
		if fileExists("/opt/homebrew/lib/libsnappy.dylib") {
			libs += " -L/opt/homebrew/lib -lsnappy"
			snappyDir = "/opt/homebrew/lib"
		}
		return "-I" + inc, libs, snappyDir, true
	}
	return "-I" + inc, "-L" + dir + " -lblosc", dir, true
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func libDirFromFlags(libs string) string {
	fields := strings.Fields(libs)
	for _, f := range fields {
		if strings.HasPrefix(f, "-L") {
			return strings.TrimPrefix(f, "-L")
		}
	}
	return ""
}

func envWithoutBlosc() []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "BLOSC_") {
			continue
		}
		env = append(env, e)
	}
	return env
}

func runInterop(t *testing.T, bin string, input []byte, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = interopEnv
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

func TestCBloscInstalledRoundTrip(t *testing.T) {
	bin := cbloscTool(t)

	codecs := []struct {
		codec Codec
		name  string
	}{
		{BloscLZ, "blosclz"},
		{LZ4, "lz4"},
		{LZ4HC, "lz4hc"},
		{ZLIB, "zlib"},
		{ZSTD, "zstd"},
		{Snappy, "snappy"},
	}
	shuffles := []struct {
		mode Shuffle
		code int
	}{
		{NoShuffle, 0},
		{Shuffle1, 1},
		{BitShuffle, 2},
	}
	sizes := []int{64, 4096, 100000, 600000}
	typeSizes := []int{4, 8}

	for _, codec := range codecs {
		for _, shuffle := range shuffles {
			for _, typeSize := range typeSizes {
				for _, size := range sizes {
					if size%typeSize != 0 {
						continue
					}
					name := fmt.Sprintf("%s/%s/ts%d/n%d", codec.name, shuffle.mode, typeSize, size)
					t.Run(name, func(t *testing.T) {
						data := makeTestData(size)
						fromC, err := runInterop(t, bin, data, "compress", "5", fmt.Sprint(shuffle.code), fmt.Sprint(typeSize), codec.name)
						if err != nil {
							msg := err.Error()
							if strings.Contains(msg, "not been compiled") || strings.Contains(msg, "compression support") {
								t.Skipf("c-blosc has no %s: %v", codec.name, err)
							}
							t.Fatal(err)
						}
						got, err := Decompress(fromC)
						if err != nil {
							t.Fatalf("go decompress of c-blosc chunk: %v", err)
						}
						if !bytes.Equal(data, got) {
							t.Fatal("go decompress of c-blosc chunk mismatched")
						}

						fromGo, err := Compress(data, codec.codec, 5, shuffle.mode, typeSize)
						if err != nil {
							t.Fatal(err)
						}
						back, err := runInterop(t, bin, fromGo, "decompress")
						if err != nil {
							t.Fatalf("c-blosc decompress of go chunk: %v", err)
						}
						if !bytes.Equal(data, back) {
							t.Fatal("c-blosc decompress of go chunk mismatched")
						}
					})
				}
			}
		}
	}
}

// TestZarrV3CBloscMatrix covers the parameter space of zarr-python BloscCodec:
// every cname and shuffle, clevel 0/1/5/9, typesize 1/4/8/16, automatic and
// explicit blocksize. clevel 0 chunks must match c-blosc byte for byte.
func TestZarrV3CBloscMatrix(t *testing.T) {
	bin := cbloscTool(t)

	codecs := []struct {
		codec Codec
		name  string
	}{
		{BloscLZ, "blosclz"},
		{LZ4, "lz4"},
		{LZ4HC, "lz4hc"},
		{ZLIB, "zlib"},
		{ZSTD, "zstd"},
		{Snappy, "snappy"},
	}
	shuffles := []struct {
		mode Shuffle
		code int
	}{
		{NoShuffle, 0},
		{Shuffle1, 1},
		{BitShuffle, 2},
	}
	levels := []int{0, 1, 5, 9}
	typeSizes := []int{1, 4, 8, 16}
	sizes := []int{64, 4096, 100000, 600000}
	blockSizes := []int{0, 4096}

	for _, codec := range codecs {
		for _, shuffle := range shuffles {
			for _, typeSize := range typeSizes {
				for _, size := range sizes {
					if size%typeSize != 0 {
						continue
					}
					for _, level := range levels {
						for _, blockSize := range blockSizes {
							name := fmt.Sprintf("%s/%s/c%d/ts%d/bs%d/n%d", codec.name, shuffle.mode, level, typeSize, blockSize, size)
							t.Run(name, func(t *testing.T) {
								data := makeTestData(size)
								args := []string{"compress", fmt.Sprint(level), fmt.Sprint(shuffle.code), fmt.Sprint(typeSize), codec.name, fmt.Sprint(blockSize)}
								fromC, err := runInterop(t, bin, data, args...)
								if err != nil {
									msg := err.Error()
									if strings.Contains(msg, "not been compiled") || strings.Contains(msg, "compression support") {
										t.Skipf("c-blosc has no %s: %v", codec.name, err)
									}
									t.Fatal(err)
								}
								got, err := Decompress(fromC)
								if err != nil {
									t.Fatalf("go decompress of c-blosc chunk: %v", err)
								}
								if !bytes.Equal(data, got) {
									t.Fatal("go decompress of c-blosc chunk mismatched")
								}

								fromGo, err := CompressWithOptions(data, Options{
									Codec:     codec.codec,
									Level:     level,
									Shuffle:   shuffle.mode,
									TypeSize:  typeSize,
									BlockSize: blockSize,
								})
								if err != nil {
									t.Fatal(err)
								}
								back, err := runInterop(t, bin, fromGo, "decompress")
								if err != nil {
									t.Fatalf("c-blosc decompress of go chunk: %v", err)
								}
								if !bytes.Equal(data, back) {
									t.Fatal("c-blosc decompress of go chunk mismatched")
								}
								if level == 0 && !bytes.Equal(fromGo, fromC) {
									t.Fatalf("clevel 0 chunks differ: go %d bytes, c-blosc %d bytes", len(fromGo), len(fromC))
								}
							})
						}
					}
				}
			}
		}
	}
}
