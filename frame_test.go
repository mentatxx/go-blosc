package blosc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCBloscFrameLayout(t *testing.T) {
	data := makeTestData(4096)
	compressed, err := Compress(data, ZSTD, 5, BitShuffle, 8)
	if err != nil {
		t.Fatal(err)
	}
	header, err := ParseHeader(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if header.VersionLZ != 1 {
		t.Fatalf("versionlz = %d, want 1", header.VersionLZ)
	}
	if header.Compressor() != ZSTD {
		t.Fatalf("compressor = %s, want zstd", header.Compressor())
	}
	if header.Flags&flagReserved != 0 {
		t.Fatal("reserved flag bit is set")
	}
	if header.Flags&flagDontSplit == 0 {
		t.Fatal("zstd chunk must set dont_split")
	}
	if header.Flags>>5 != 4 {
		t.Fatalf("compformat = %d, want 4", header.Flags>>5)
	}
	if header.IsMemcpy() {
		t.Fatal("patterned data should compress")
	}

	nblocks, _ := blockCount(int(header.NBytesOrig), int(header.BlockSize))
	bstart := binary.LittleEndian.Uint32(compressed[HeaderSize:])
	if bstart != uint32(HeaderSize+4*nblocks) {
		t.Fatalf("bstart[0] = %d, want %d", bstart, HeaderSize+4*nblocks)
	}

	decompressed, err := Decompress(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, decompressed) {
		t.Fatal("round-trip mismatch")
	}
}

func TestSmallBufferIsMemcpy(t *testing.T) {
	data := bytes.Repeat([]byte{1, 2, 3, 4}, 16) // 64 bytes
	compressed, err := Compress(data, ZSTD, 9, BitShuffle, 4)
	if err != nil {
		t.Fatal(err)
	}
	header, err := ParseHeader(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !header.IsMemcpy() {
		t.Fatal("buffers shorter than 128 bytes must be memcpy, matching c-blosc")
	}
	if int(header.NBytesComp) != len(data)+HeaderSize {
		t.Fatalf("cbytes = %d, want %d", header.NBytesComp, len(data)+HeaderSize)
	}
	got, err := Decompress(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, got) {
		t.Fatal("memcpy round-trip mismatch")
	}
}

func TestCLevel0IsMemcpy(t *testing.T) {
	data := makeTestData(4096)
	compressed, err := Compress(data, LZ4, 0, Shuffle1, 4)
	if err != nil {
		t.Fatal(err)
	}
	header, err := ParseHeader(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !header.IsMemcpy() {
		t.Fatal("clevel 0 must be a memcpy chunk")
	}
	if !header.HasShuffle() {
		t.Fatal("clevel 0 still records the requested shuffle flag")
	}
	if int(header.NBytesComp) != len(data)+HeaderSize {
		t.Fatalf("cbytes = %d, want %d", header.NBytesComp, len(data)+HeaderSize)
	}
	if !bytes.Equal(compressed[HeaderSize:], data) {
		t.Fatal("clevel 0 payload must be the original bytes")
	}
	wantBlock := computeBlockSize(LZ4, 0, 4, len(data), 0)
	if int(header.BlockSize) != wantBlock {
		t.Fatalf("blocksize = %d, want %d", header.BlockSize, wantBlock)
	}
	got, err := Decompress(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, got) {
		t.Fatal("clevel 0 round-trip mismatch")
	}
}

func TestMultiBlockRoundTrip(t *testing.T) {
	// Larger than the zstd level-5 block (256 KiB) so the chunk has several blocks.
	data := makeTestData(600000)
	for _, shuffle := range []Shuffle{NoShuffle, Shuffle1, BitShuffle} {
		t.Run(shuffle.String(), func(t *testing.T) {
			compressed, err := Compress(data, ZSTD, 5, shuffle, 8)
			if err != nil {
				t.Fatal(err)
			}
			header, err := ParseHeader(compressed)
			if err != nil {
				t.Fatal(err)
			}
			nblocks, _ := blockCount(len(data), int(header.BlockSize))
			if nblocks < 2 {
				t.Fatalf("nblocks = %d, want at least 2 (blocksize %d)", nblocks, header.BlockSize)
			}
			got, err := Decompress(compressed)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, got) {
				t.Fatal("multi-block round-trip mismatch")
			}
		})
	}
}

func TestLegacySingleBlobStillReads(t *testing.T) {
	data := makeTestData(4096)
	shuffled := shuffleBytes(data, 4)
	codec, ok := GetCodec(ZSTD)
	if !ok {
		t.Fatal("zstd codec missing")
	}
	payload, err := codec.Compress(shuffled, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) >= len(data) {
		t.Fatal("fixture did not compress")
	}

	chunk := make([]byte, HeaderSize+len(payload))
	chunk[0] = FormatVersion
	chunk[1] = byte(ZSTD) // legacy: codec id in versionlz
	chunk[2] = flagShuffle
	chunk[3] = 4
	binary.LittleEndian.PutUint32(chunk[4:8], uint32(len(data)))
	binary.LittleEndian.PutUint32(chunk[8:12], uint32(len(data)))
	binary.LittleEndian.PutUint32(chunk[12:16], uint32(len(chunk)))
	copy(chunk[HeaderSize:], payload)

	header, err := ParseHeader(chunk)
	if err != nil {
		t.Fatal(err)
	}
	if header.Compressor() != ZSTD {
		t.Fatalf("legacy compressor = %s, want zstd", header.Compressor())
	}
	got, err := Decompress(chunk)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, got) {
		t.Fatal("legacy chunk did not round-trip")
	}
}

func TestReservedFlagRejected(t *testing.T) {
	data := makeTestData(256)
	compressed, err := Compress(data, LZ4, 5, NoShuffle, 1)
	if err != nil {
		t.Fatal(err)
	}
	compressed[2] |= flagReserved
	_, err = Decompress(compressed)
	if !errors.Is(err, ErrInvalidData) {
		t.Fatalf("got %v, want ErrInvalidData", err)
	}
}

func TestBitShuffleNotMultipleOfEightIsIdentity(t *testing.T) {
	data := makeTestData(28) // 7 elements of 4 bytes
	if !bytes.Equal(data, bitShuffle(data, 4)) {
		t.Fatal("element count not divisible by 8 must be left unchanged")
	}
}

func TestBloscLZRoundTrip(t *testing.T) {
	for _, size := range []int{64, 4096, 100000} {
		data := makeTestData(size)
		compressed, err := Compress(data, BloscLZ, 5, Shuffle1, 4)
		if err != nil {
			t.Fatal(err)
		}
		header, err := ParseHeader(compressed)
		if err != nil {
			t.Fatal(err)
		}
		if header.VersionLZ != 1 || header.Compressor() != BloscLZ {
			t.Fatalf("header codec = versionlz %d %s", header.VersionLZ, header.Compressor())
		}
		got, err := Decompress(compressed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, got) {
			t.Fatalf("size %d did not round-trip", size)
		}
	}

	noisy := make([]byte, 4096)
	for i := range noisy {
		noisy[i] = byte(i*37 + i*i)
	}
	compressed, err := Compress(noisy, BloscLZ, 9, NoShuffle, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decompress(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(noisy, got) {
		t.Fatal("incompressible BloscLZ buffer did not round-trip")
	}
}

func TestCBloscFixtures(t *testing.T) {
	matches, err := filepath.Glob("testdata/cblosc/*.blosc")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no c-blosc fixtures in testdata/cblosc")
	}
	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			base := strings.TrimSuffix(filepath.Base(path), ".blosc")
			parts := strings.Split(base, "_")
			size, err := strconv.Atoi(parts[len(parts)-1])
			if err != nil {
				t.Fatal(err)
			}
			got, err := Decompress(raw)
			if err != nil {
				t.Fatalf("decompress c-blosc fixture: %v", err)
			}
			if !bytes.Equal(got, makeTestData(size)) {
				t.Fatal("fixture did not match the original bytes")
			}
		})
	}
}
