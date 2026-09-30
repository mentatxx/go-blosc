package blosc

func init() {
	initSIMD()
}

// shuffleBytes performs byte-level shuffle on data.
//
// For an array of N elements with typeSize bytes each, the shuffle rearranges
// bytes so that all first bytes of each element are together, then all second
// bytes, etc. This improves compression for typed data because similar bytes
// (e.g., exponent bits of floats) are grouped together.
//
// Example for 4-byte elements [A0 A1 A2 A3] [B0 B1 B2 B3] [C0 C1 C2 C3]:
// After shuffle: [A0 B0 C0] [A1 B1 C1] [A2 B2 C2] [A3 B3 C3]
func shuffleBytes(src []byte, typeSize int) []byte {
	if typeSize <= 1 || len(src) < typeSize {
		return src
	}

	n := len(src)
	numElements := n / typeSize
	dst := make([]byte, n)

	// Try SIMD acceleration for typeSize=4
	if typeSize == 4 {
		var usedSIMD bool
		var chunkElements int

		// Try AVX2 (processes 8 elements = 32 bytes at a time)
		if useAVX2 && n >= 32 {
			usedSIMD = shuffleBytesAVX2(dst, src, typeSize)
			chunkElements = 8
		}

		// Try NEON (processes 4 elements = 16 bytes at a time)
		if !usedSIMD && useNEON && n >= 16 {
			usedSIMD = shuffleBytesNEON(dst, src, typeSize)
			chunkElements = 4
		}

		if usedSIMD {
			// SIMD processed full chunks, handle remainder elements
			processedElements := (numElements / chunkElements) * chunkElements
			for i := processedElements; i < numElements; i++ {
				for j := 0; j < typeSize; j++ {
					dst[j*numElements+i] = src[i*typeSize+j]
				}
			}
			// Handle remaining bytes (if any)
			remainder := n % typeSize
			if remainder > 0 {
				copy(dst[numElements*typeSize:], src[numElements*typeSize:])
			}
			return dst
		}
	}

	// Generic implementation
	for i := 0; i < numElements; i++ {
		for j := 0; j < typeSize; j++ {
			dst[j*numElements+i] = src[i*typeSize+j]
		}
	}

	// Handle remaining bytes (if any)
	remainder := n % typeSize
	if remainder > 0 {
		copy(dst[numElements*typeSize:], src[numElements*typeSize:])
	}

	return dst
}

// unshuffleBytes reverses the byte-level shuffle operation.
func unshuffleBytes(src []byte, typeSize int) []byte {
	if typeSize <= 1 || len(src) < typeSize {
		return src
	}

	n := len(src)
	numElements := n / typeSize
	dst := make([]byte, n)

	// Try SIMD acceleration for typeSize=4
	if typeSize == 4 {
		var usedSIMD bool
		var chunkElements int

		// Try AVX2 (processes 8 elements = 32 bytes at a time)
		if useAVX2 && n >= 32 {
			usedSIMD = unshuffleBytesAVX2(dst, src, typeSize)
			chunkElements = 8
		}

		// Try NEON (processes 4 elements = 16 bytes at a time)
		if !usedSIMD && useNEON && n >= 16 {
			usedSIMD = unshuffleBytesNEON(dst, src, typeSize)
			chunkElements = 4
		}

		if usedSIMD {
			// SIMD processed full chunks, handle remainder elements
			processedElements := (numElements / chunkElements) * chunkElements
			for i := processedElements; i < numElements; i++ {
				for j := 0; j < typeSize; j++ {
					dst[i*typeSize+j] = src[j*numElements+i]
				}
			}
			// Handle remaining bytes (if any)
			remainder := n % typeSize
			if remainder > 0 {
				copy(dst[numElements*typeSize:], src[numElements*typeSize:])
			}
			return dst
		}
	}

	// Generic implementation
	for i := 0; i < numElements; i++ {
		for j := 0; j < typeSize; j++ {
			dst[i*typeSize+j] = src[j*numElements+i]
		}
	}

	// Handle remaining bytes (if any)
	remainder := n % typeSize
	if remainder > 0 {
		copy(dst[numElements*typeSize:], src[numElements*typeSize:])
	}

	return dst
}

// ShuffleBuffer performs shuffle in-place on a buffer
func ShuffleBuffer(data []byte, typeSize int, mode Shuffle) {
	var result []byte
	switch mode {
	case Shuffle1:
		result = shuffleBytes(data, typeSize)
	case BitShuffle:
		result = bitShuffle(data, typeSize)
	default:
		return
	}
	copy(data, result)
}

// UnshuffleBuffer performs unshuffle in-place on a buffer
func UnshuffleBuffer(data []byte, typeSize int, mode Shuffle) {
	var result []byte
	switch mode {
	case Shuffle1:
		result = unshuffleBytes(data, typeSize)
	case BitShuffle:
		result = bitUnshuffle(data, typeSize)
	default:
		return
	}
	copy(data, result)
}
