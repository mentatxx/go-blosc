package blosc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Layout constants shared with c-blosc 1.x (FORWARD_COMPAT split mode).
const (
	codecFormatVersion = 1
	minBufferSize      = 128
	maxSplits          = 16
	l1Size             = 32 * 1024
	maxBufferSize      = math.MaxInt32 - HeaderSize
	maxBlockSize       = (math.MaxInt32 - 255*4) / 3
)

var errDoesNotFit = errors.New("blosc: compressed chunk exceeds c-blosc size limit")

func compFormat(codec Codec) (uint8, error) {
	switch codec {
	case BloscLZ:
		return 0, nil
	case LZ4, LZ4HC:
		return 1, nil
	case Snappy:
		return 2, nil
	case ZLIB:
		return 3, nil
	case ZSTD:
		return 4, nil
	default:
		return 0, fmt.Errorf("%w: %s", ErrInvalidCodec, codec)
	}
}

func codecFromFormat(format uint8) (Codec, error) {
	switch format {
	case 0:
		return BloscLZ, nil
	case 1:
		return LZ4, nil
	case 2:
		return Snappy, nil
	case 3:
		return ZLIB, nil
	case 4:
		return ZSTD, nil
	default:
		return 0, fmt.Errorf("%w: format %d", ErrInvalidCodec, format)
	}
}

func isHCR(codec Codec) bool {
	return codec == LZ4HC || codec == ZLIB || codec == ZSTD
}

// splitBlock reports whether c-blosc's default FORWARD_COMPAT mode splits a block.
func splitBlock(codec Codec, typesize, blocksize int) bool {
	if typesize <= 0 {
		return false
	}
	return codec != ZSTD && typesize <= maxSplits && blocksize/typesize >= minBufferSize
}

// computeBlockSize matches c-blosc compute_blocksize.
func computeBlockSize(codec Codec, clevel, typesize, nbytes, forced int) int {
	if nbytes < typesize {
		return 1
	}

	blocksize := nbytes
	if forced > 0 {
		blocksize = forced
		if blocksize < minBufferSize {
			blocksize = minBufferSize
		}
		if blocksize > maxBlockSize {
			blocksize = maxBlockSize
		}
	} else if nbytes >= l1Size {
		blocksize = l1Size
		if isHCR(codec) {
			blocksize *= 2
		}
		switch clevel {
		case 0:
			blocksize /= 4
		case 1:
			blocksize /= 2
		case 2:
			// clevel 2 keeps the L1-sized block.
		case 3:
			blocksize *= 2
		case 4, 5:
			blocksize *= 4
		case 6, 7, 8:
			blocksize *= 8
		case 9:
			blocksize *= 8
			if isHCR(codec) {
				blocksize *= 2
			}
		}
	}

	if clevel > 0 && splitBlock(codec, typesize, blocksize) {
		if blocksize > (1 << 18) {
			blocksize = 1 << 18
		}
		blocksize *= typesize
		if blocksize < (1 << 16) {
			blocksize = 1 << 16
		}
		if blocksize > 1024*1024 {
			blocksize = 1024 * 1024
		}
	}

	if blocksize > nbytes {
		blocksize = nbytes
	}
	if blocksize > typesize {
		blocksize = blocksize / typesize * typesize
	}
	return blocksize
}

func splitsForBlock(flags uint8, typesize, blockBytes int, leftover bool) int {
	dontSplit := flags&flagDontSplit != 0
	if !dontSplit && typesize > 0 && typesize <= maxSplits && blockBytes/typesize >= minBufferSize && !leftover {
		return typesize
	}
	return 1
}

func shuffleFlags(mode Shuffle) uint8 {
	switch mode {
	case Shuffle1:
		return flagShuffle
	case BitShuffle:
		return flagBitShuffle
	default:
		return 0
	}
}

func compressChunk(data []byte, opts Options) ([]byte, error) {
	if len(data) > maxBufferSize {
		return nil, ErrDataTooLarge
	}
	format, err := compFormat(opts.Codec)
	if err != nil {
		return nil, err
	}
	compressor, ok := codecs[opts.Codec]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrInvalidCodec, opts.Codec)
	}

	typesize := opts.TypeSize
	if typesize > 255 {
		typesize = 1
	}

	blocksize := computeBlockSize(opts.Codec, opts.Level, typesize, len(data), opts.BlockSize)
	flags := shuffleFlags(opts.Shuffle)
	if !splitBlock(opts.Codec, typesize, blocksize) {
		flags |= flagDontSplit
	}
	flags |= format << 5

	// c-blosc stores clevel 0 as an uncompressed memcpy, shuffle flags and
	// all. The same path is used for buffers below the minimum block size.
	if opts.Level == 0 || len(data) < minBufferSize {
		return memcpyChunk(data, flags, typesize, blocksize), nil
	}

	chunk, err := encodeBlocks(data, compressor, opts, typesize, blocksize, flags)
	if errors.Is(err, errDoesNotFit) {
		return memcpyChunk(data, flags, typesize, blocksize), nil
	}
	return chunk, err
}

func memcpyChunk(data []byte, flags uint8, typesize, blocksize int) []byte {
	out := make([]byte, HeaderSize+len(data))
	out[0] = FormatVersion
	out[1] = codecFormatVersion
	out[2] = flags | flagMemcpy
	out[3] = byte(typesize)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(data)))
	binary.LittleEndian.PutUint32(out[8:12], uint32(blocksize))
	binary.LittleEndian.PutUint32(out[12:16], uint32(len(out)))
	copy(out[HeaderSize:], data)
	return out
}

func encodeBlocks(data []byte, compressor CodecInterface, opts Options, typesize, blocksize int, flags uint8) ([]byte, error) {
	nbytes := len(data)
	nblocks, leftover := blockCount(nbytes, blocksize)
	limit := nbytes + HeaderSize
	overhead := HeaderSize + 4*nblocks
	if nblocks <= 0 || overhead >= limit {
		return nil, errDoesNotFit
	}

	out := make([]byte, overhead, limit)
	offset := overhead
	for j := 0; j < nblocks; j++ {
		binary.LittleEndian.PutUint32(out[HeaderSize+4*j:], uint32(offset))
		bsize := blocksize
		isLeftover := false
		start := j * blocksize
		if j == nblocks-1 && leftover > 0 {
			bsize = leftover
			isLeftover = true
		}
		block := data[start : start+bsize]
		filtered := filterBlock(block, opts.Shuffle, typesize)
		nsplits := splitsForBlock(flags, typesize, bsize, isLeftover)
		if nsplits <= 0 || bsize/nsplits == 0 || nsplits*(bsize/nsplits) != bsize {
			return nil, ErrInvalidData
		}
		neblock := bsize / nsplits
		for s := 0; s < nsplits; s++ {
			part := filtered[s*neblock : (s+1)*neblock]
			var compressed []byte
			var err error
			if sc, ok := compressor.(splittingCompressor); ok {
				compressed, err = sc.CompressSplit(part, opts.Level, flags&flagDontSplit == 0)
			} else {
				compressed, err = compressor.Compress(part, opts.Level)
			}
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrCompressionFailed, err)
			}
			stored := compressed
			if len(stored) == 0 || len(stored) >= neblock {
				stored = part
			}
			need := 4 + len(stored)
			if offset+need > limit {
				return nil, errDoesNotFit
			}
			var csize [4]byte
			binary.LittleEndian.PutUint32(csize[:], uint32(len(stored)))
			out = append(out, csize[:]...)
			out = append(out, stored...)
			offset += need
		}
	}

	out[0] = FormatVersion
	out[1] = codecFormatVersion
	out[2] = flags
	out[3] = byte(typesize)
	binary.LittleEndian.PutUint32(out[4:8], uint32(nbytes))
	binary.LittleEndian.PutUint32(out[8:12], uint32(blocksize))
	binary.LittleEndian.PutUint32(out[12:16], uint32(len(out)))
	return out, nil
}

func blockCount(nbytes, blocksize int) (nblocks, leftover int) {
	if blocksize <= 0 {
		return 0, 0
	}
	nblocks = nbytes / blocksize
	leftover = nbytes % blocksize
	if leftover > 0 {
		nblocks++
	}
	return nblocks, leftover
}

func filterBlock(block []byte, mode Shuffle, typesize int) []byte {
	switch mode {
	case Shuffle1:
		if typesize > 1 {
			return shuffleBytes(block, typesize)
		}
	case BitShuffle:
		if typesize > 0 && len(block) >= typesize {
			return bitShuffle(block, typesize)
		}
	}
	return block
}

func decompressChunk(data []byte, typeSize int) ([]byte, error) {
	header, err := ParseHeader(data)
	if err != nil {
		return nil, err
	}
	if header.Flags&flagReserved != 0 {
		return nil, ErrInvalidData
	}
	if int(header.NBytesComp) > len(data) || header.NBytesComp < HeaderSize {
		return nil, ErrInvalidData
	}
	if header.NBytesOrig == 0 {
		return []byte{}, nil
	}
	if header.TypeSize == 0 || header.BlockSize == 0 ||
		header.BlockSize > header.NBytesOrig || header.BlockSize > uint32(maxBlockSize) {
		return nil, ErrInvalidData
	}

	chunk := data[:header.NBytesComp]
	if header.IsMemcpy() {
		if int(header.NBytesOrig)+HeaderSize != int(header.NBytesComp) {
			return nil, ErrInvalidData
		}
		out := make([]byte, header.NBytesOrig)
		copy(out, chunk[HeaderSize:])
		return out, nil
	}

	format := header.Flags >> 5
	if format == 0 && header.VersionLZ != codecFormatVersion {
		return decompressLegacy(chunk, header, Codec(header.VersionLZ), typeSize)
	}
	if format == 0 && header.VersionLZ == codecFormatVersion {
		if chunkLooksStructured(chunk, header) {
			return decompressBlocks(chunk, header, BloscLZ, typeSize)
		}
		return decompressLegacy(chunk, header, LZ4, typeSize)
	}
	if header.VersionLZ != codecFormatVersion {
		return nil, fmt.Errorf("%w: versionlz %d", ErrInvalidCodec, header.VersionLZ)
	}
	codec, err := codecFromFormat(format)
	if err != nil {
		return nil, err
	}
	return decompressBlocks(chunk, header, codec, typeSize)
}

func decompressLegacy(data []byte, header *Header, codec Codec, typeSize int) ([]byte, error) {
	decompressor, ok := codecs[codec]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrInvalidCodec, codec)
	}
	raw, err := decompressor.Decompress(data[HeaderSize:], int(header.NBytesOrig))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecompressionFailed, err)
	}
	if len(raw) != int(header.NBytesOrig) {
		return nil, fmt.Errorf("%w: got %d, expected %d", ErrSizeMismatch, len(raw), header.NBytesOrig)
	}
	out := make([]byte, len(raw))
	unfilterInto(out, raw, header.Flags, int(header.TypeSize), typeSize)
	return out, nil
}

func decompressBlocks(data []byte, header *Header, codec Codec, typeSize int) ([]byte, error) {
	decompressor, ok := codecs[codec]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrInvalidCodec, codec)
	}

	nbytes := int(header.NBytesOrig)
	blocksize := int(header.BlockSize)
	cbytes := int(header.NBytesComp)
	typesize := int(header.TypeSize)
	nblocks, leftover := blockCount(nbytes, blocksize)
	if nblocks <= 0 || nblocks > (cbytes-HeaderSize)/4 {
		return nil, ErrInvalidData
	}

	out := make([]byte, nbytes)
	for j := 0; j < nblocks; j++ {
		bstart := int(binary.LittleEndian.Uint32(data[HeaderSize+4*j:]))
		bsize := blocksize
		isLeftover := false
		if j == nblocks-1 && leftover > 0 {
			bsize = leftover
			isLeftover = true
		}
		nsplits := splitsForBlock(header.Flags, typesize, bsize, isLeftover)
		neblock := 0
		if nsplits > 0 {
			neblock = bsize / nsplits
		}
		if neblock <= 0 || nsplits*neblock != bsize {
			return nil, ErrInvalidData
		}

		filtered := make([]byte, bsize)
		off := bstart
		for s := 0; s < nsplits; s++ {
			if off < 0 || off > cbytes-4 {
				return nil, ErrInvalidData
			}
			csize := int(binary.LittleEndian.Uint32(data[off:]))
			off += 4
			if csize < 0 || csize > cbytes-off {
				return nil, ErrInvalidData
			}
			dst := filtered[s*neblock : (s+1)*neblock]
			if csize == neblock {
				copy(dst, data[off:off+csize])
			} else {
				plain, err := decompressor.Decompress(data[off:off+csize], neblock)
				if err != nil {
					return nil, fmt.Errorf("%w: %v", ErrDecompressionFailed, err)
				}
				if len(plain) != neblock {
					return nil, fmt.Errorf("%w: got %d, expected %d", ErrSizeMismatch, len(plain), neblock)
				}
				copy(dst, plain)
			}
			off += csize
		}

		start := j * blocksize
		unfilterInto(out[start:start+bsize], filtered, header.Flags, typesize, typeSize)
	}
	return out, nil
}

// chunkLooksStructured reports whether a compformat=0 chunk has a c-blosc
// bstarts/split layout. Those chunks are BloscLZ. A legacy single-blob LZ4
// chunk fails the walk and is decoded as LZ4.
func chunkLooksStructured(data []byte, header *Header) bool {
	nbytes := int(header.NBytesOrig)
	blocksize := int(header.BlockSize)
	cbytes := len(data)
	typesize := int(header.TypeSize)
	nblocks, leftover := blockCount(nbytes, blocksize)
	if nblocks <= 0 || nblocks > (cbytes-HeaderSize)/4 {
		return false
	}
	off := HeaderSize + 4*nblocks
	for j := 0; j < nblocks; j++ {
		bstart := int(binary.LittleEndian.Uint32(data[HeaderSize+4*j:]))
		if bstart != off {
			return false
		}
		bsize := blocksize
		isLeftover := false
		if j == nblocks-1 && leftover > 0 {
			bsize = leftover
			isLeftover = true
		}
		nsplits := splitsForBlock(header.Flags, typesize, bsize, isLeftover)
		neblock := 0
		if nsplits > 0 {
			neblock = bsize / nsplits
		}
		if neblock <= 0 || nsplits*neblock != bsize {
			return false
		}
		for s := 0; s < nsplits; s++ {
			if off > cbytes-4 {
				return false
			}
			csize := int(binary.LittleEndian.Uint32(data[off:]))
			off += 4
			if csize <= 0 || csize > cbytes-off {
				return false
			}
			off += csize
		}
	}
	return off == cbytes
}

func unfilterInto(dst, block []byte, flags uint8, headerTypeSize, typeSize int) {
	ts := typeSize
	if ts <= 0 {
		ts = headerTypeSize
	}
	var plain []byte
	switch {
	case flags&flagShuffle != 0 && ts > 1:
		plain = unshuffleBytes(block, ts)
	case flags&flagBitShuffle != 0 && ts > 0 && len(block) >= ts:
		plain = bitUnshuffle(block, ts)
	default:
		plain = block
	}
	copy(dst, plain)
}
