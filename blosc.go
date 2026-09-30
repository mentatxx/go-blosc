// Package blosc provides a pure Go implementation of the Blosc compression format.
//
// Blosc is a high-performance compressor optimized for binary data, commonly used
// in scientific computing and VFX applications. It combines shuffle/bitshuffle
// preprocessing with fast compression codecs (BloscLZ, LZ4, ZSTD, ZLIB, Snappy) to achieve
// excellent compression ratios and speed for typed array data.
//
// # Basic Usage
//
//	// Compress data
//	compressed, err := blosc.Compress(data, blosc.LZ4, 5, blosc.Shuffle, 4)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Decompress data
//	decompressed, err := blosc.Decompress(compressed)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// # Shuffle Modes
//
// Blosc supports three shuffle modes that rearrange bytes before compression:
//
//   - NoShuffle: No preprocessing, data compressed as-is
//   - Shuffle: Byte shuffle - groups bytes by position within elements
//   - BitShuffle: Bit-level shuffle for maximum compression of typed data
//
// # Supported Codecs
//
//   - BloscLZ: c-blosc default codec
//   - LZ4: Very fast compression/decompression (default)
//   - ZSTD: High compression ratio with good speed
//   - ZLIB: Standard deflate compression
//   - Snappy: Google's fast compression codec
//
// # Thread Safety
//
// All functions in this package are safe for concurrent use.
package blosc

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Version constants
const (
	Version       = "1.0.4"
	FormatVersion = 2 // Blosc format version
)

// Codec identifies the compression algorithm
type Codec uint8

const (
	BloscLZ Codec = iota // BloscLZ, the default c-blosc codec
	LZ4                  // LZ4 compression
	LZ4HC                // LZ4 High Compression
	Snappy               // Snappy compression
	ZLIB                 // ZLIB/deflate compression
	ZSTD                 // Zstandard compression
)

// String returns the codec name
func (c Codec) String() string {
	switch c {
	case BloscLZ:
		return "blosclz"
	case LZ4:
		return "lz4"
	case LZ4HC:
		return "lz4hc"
	case Snappy:
		return "snappy"
	case ZLIB:
		return "zlib"
	case ZSTD:
		return "zstd"
	default:
		return fmt.Sprintf("unknown(%d)", c)
	}
}

// Shuffle mode for byte/bit reordering
type Shuffle uint8

const (
	NoShuffle  Shuffle = 0x0 // No shuffle
	Shuffle1   Shuffle = 0x1 // Byte shuffle
	BitShuffle Shuffle = 0x2 // Bit shuffle
)

// String returns the shuffle mode name
func (s Shuffle) String() string {
	switch s {
	case NoShuffle:
		return "noshuffle"
	case Shuffle1:
		return "shuffle"
	case BitShuffle:
		return "bitshuffle"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

// Flag bits in the Blosc header. Bits 5–7 hold the on-disk compressor format,
// which is not the same numbering as Codec.
const (
	flagShuffle    = 0x01 // Byte shuffle enabled
	flagMemcpy     = 0x02 // Data stored uncompressed (memcpy)
	flagBitShuffle = 0x04 // Bit shuffle enabled
	flagReserved   = 0x08 // Reserved; c-blosc rejects chunks with this bit set
	flagDontSplit  = 0x10 // Block is not split into typesize sub-blocks
)

// Header size constants
const (
	HeaderSize    = 16 // Blosc header size in bytes
	MinHeaderSize = 16
)

// Predefined errors for common failure conditions.
// These can be checked using errors.Is() for programmatic error handling.
var (
	// ErrInvalidData indicates the compressed data is malformed or corrupted.
	ErrInvalidData = errors.New("blosc: invalid compressed data")

	// ErrInvalidHeader indicates the Blosc header is missing or malformed.
	ErrInvalidHeader = errors.New("blosc: invalid header")

	// ErrInvalidVersion indicates an unsupported Blosc format version.
	ErrInvalidVersion = errors.New("blosc: unsupported format version")

	// ErrInvalidCodec indicates the codec specified is not supported or registered.
	ErrInvalidCodec = errors.New("blosc: unsupported codec")

	// ErrSizeMismatch indicates the decompressed size does not match the expected size.
	ErrSizeMismatch = errors.New("blosc: decompressed size mismatch")

	// ErrDataTooLarge indicates the input data exceeds the maximum supported size.
	ErrDataTooLarge = errors.New("blosc: data too large")

	// ErrCompressionFailed indicates the compression operation failed.
	ErrCompressionFailed = errors.New("blosc: compression failed")

	// ErrDecompressionFailed indicates the decompression operation failed.
	ErrDecompressionFailed = errors.New("blosc: decompression failed")
)

// Header represents the 16-byte Blosc frame header that prefixes all compressed data.
// It contains metadata needed to decompress the data, including the codec used,
// shuffle mode, and original/compressed sizes.
type Header struct {
	Version    uint8  // Blosc format version (2 for current format)
	VersionLZ  uint8  // Compressor format version (1 for c-blosc codecs; legacy chunks stored the codec id here)
	Flags      uint8  // Shuffle, memcpy, split, and compressor-format flags
	TypeSize   uint8  // Element size for shuffle (1, 2, 4, 8, etc.)
	NBytesOrig uint32 // Original (uncompressed) data size
	BlockSize  uint32 // Block size used for compression
	NBytesComp uint32 // Total compressed size (including this header)
}

// ParseHeader parses a Blosc header from bytes
func ParseHeader(data []byte) (*Header, error) {
	if len(data) < HeaderSize {
		return nil, ErrInvalidHeader
	}

	h := &Header{
		Version:    data[0],
		VersionLZ:  data[1],
		Flags:      data[2],
		TypeSize:   data[3],
		NBytesOrig: binary.LittleEndian.Uint32(data[4:8]),
		BlockSize:  binary.LittleEndian.Uint32(data[8:12]),
		NBytesComp: binary.LittleEndian.Uint32(data[12:16]),
	}

	if h.Version != FormatVersion {
		return nil, fmt.Errorf("%w: got %d, expected %d", ErrInvalidVersion, h.Version, FormatVersion)
	}

	return h, nil
}

// Bytes serializes the header to bytes
func (h *Header) Bytes() []byte {
	buf := make([]byte, HeaderSize)
	buf[0] = h.Version
	buf[1] = h.VersionLZ
	buf[2] = h.Flags
	buf[3] = h.TypeSize
	binary.LittleEndian.PutUint32(buf[4:8], h.NBytesOrig)
	binary.LittleEndian.PutUint32(buf[8:12], h.BlockSize)
	binary.LittleEndian.PutUint32(buf[12:16], h.NBytesComp)
	return buf
}

// HasShuffle returns true if byte shuffle is enabled
func (h *Header) HasShuffle() bool {
	return h.Flags&flagShuffle != 0
}

// HasBitShuffle returns true if bit shuffle is enabled
func (h *Header) HasBitShuffle() bool {
	return h.Flags&flagBitShuffle != 0
}

// IsMemcpy returns true if data is stored uncompressed
func (h *Header) IsMemcpy() bool {
	return h.Flags&flagMemcpy != 0
}

// ShuffleMode returns the shuffle mode from flags
func (h *Header) ShuffleMode() Shuffle {
	if h.HasBitShuffle() {
		return BitShuffle
	}
	if h.HasShuffle() {
		return Shuffle1
	}
	return NoShuffle
}

// Compressor returns the codec stored in the chunk header.
//
// c-blosc keeps the codec in flag bits 5–7 and writes versionlz = 1.
// Chunks written by go-blosc before the c-blosc frame fix stored the codec
// id in versionlz and left those flag bits clear. When both versionlz and
// the format bits are the blosclz values (1 and 0), this reports BloscLZ;
// the decompressor still accepts a legacy single-block LZ4 chunk in that
// ambiguous case.
func (h *Header) Compressor() Codec {
	format := h.Flags >> 5
	if format != 0 {
		codec, err := codecFromFormat(format)
		if err != nil {
			return Codec(format)
		}
		return codec
	}
	if h.VersionLZ != codecFormatVersion {
		return Codec(h.VersionLZ)
	}
	return BloscLZ
}

// Options configures Blosc compression behavior.
type Options struct {
	Codec      Codec   // Compression codec (LZ4, ZSTD, ZLIB, Snappy)
	Level      int     // Compression level (0-9). 0 stores the buffer uncompressed, as c-blosc does.
	Shuffle    Shuffle // Shuffle mode (NoShuffle, Shuffle1, BitShuffle)
	TypeSize   int     // Element size in bytes for shuffle (1, 2, 4, 8)
	BlockSize  int     // Block size in bytes (0 = automatic)
	NumThreads int     // Reserved for future use (not used in pure Go implementation)
}

// DefaultOptions returns default compression options
func DefaultOptions() Options {
	return Options{
		Codec:     LZ4,
		Level:     5,
		Shuffle:   Shuffle1,
		TypeSize:  4,
		BlockSize: 0,
	}
}

// Compress compresses data using Blosc format
//
// Parameters:
//   - data: Input data to compress
//   - codec: Compression codec (LZ4, ZSTD, ZLIB, Snappy)
//   - level: Compression level (0-9, where 0 stores the buffer uncompressed)
//   - shuffle: Shuffle mode (NoShuffle, Shuffle1, BitShuffle)
//   - typeSize: Element size for shuffle preprocessing (1, 2, 4, 8 bytes)
//
// Returns compressed data with Blosc header, or error
func Compress(data []byte, codec Codec, level int, shuffle Shuffle, typeSize int) ([]byte, error) {
	opts := Options{
		Codec:    codec,
		Level:    level,
		Shuffle:  shuffle,
		TypeSize: typeSize,
	}
	return CompressWithOptions(data, opts)
}

// CompressWithOptions compresses data using specified options.
func CompressWithOptions(data []byte, opts Options) ([]byte, error) {
	if len(data) == 0 {
		return nil, ErrInvalidData
	}

	// Validate options
	if opts.TypeSize <= 0 {
		opts.TypeSize = 1
	}
	// c-blosc rejects levels outside 0..9. Level 0 is a memcpy chunk and must
	// not be promoted to 1: Zarr's BloscCodec uses 0 for "no compression".
	if opts.Level < 0 {
		opts.Level = 1
	}
	if opts.Level > 9 {
		opts.Level = 9
	}

	// Call backend implementation (pure Go or CGO depending on build tags)
	return compressBackend(data, opts)
}

// Decompress decompresses Blosc-compressed data
//
// The typeSize parameter is optional - if 0, it uses the typeSize from the header
func Decompress(data []byte) ([]byte, error) {
	return DecompressWithSize(data, 0)
}

// DecompressWithSize decompresses with explicit type size override.
func DecompressWithSize(data []byte, typeSize int) ([]byte, error) {
	if len(data) < HeaderSize {
		return nil, ErrInvalidHeader
	}

	// Call backend implementation (pure Go or CGO depending on build tags)
	return decompressBackend(data, typeSize)
}

// GetInfo returns information about compressed data without decompressing
func GetInfo(data []byte) (*Header, error) {
	return ParseHeader(data)
}

// GetDecompressedSize returns the original size of compressed data
func GetDecompressedSize(data []byte) (int, error) {
	header, err := ParseHeader(data)
	if err != nil {
		return 0, err
	}
	return int(header.NBytesOrig), nil
}

// compressBackend implements compression using pure Go codecs.
func compressBackend(data []byte, opts Options) ([]byte, error) {
	return compressChunk(data, opts)
}

// decompressBackend implements decompression using pure Go codecs.
func decompressBackend(data []byte, typeSize int) ([]byte, error) {
	return decompressChunk(data, typeSize)
}
