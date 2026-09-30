package blosc

import "encoding/binary"

// bitShuffle matches c-blosc bshuf_trans_bit_elem on little-endian hosts:
// byte transpose, then bit transpose, then a transpose of bit rows in groups of 8.
// If the element count is not a multiple of 8 the block is left unchanged, as in c-blosc.
func bitShuffle(src []byte, typeSize int) []byte {
	return applyBitShuffle(src, typeSize, false)
}

// bitUnshuffle reverses bitShuffle.
func bitUnshuffle(src []byte, typeSize int) []byte {
	return applyBitShuffle(src, typeSize, true)
}

func applyBitShuffle(src []byte, typeSize int, unshuffle bool) []byte {
	if typeSize < 1 || len(src) < typeSize {
		return src
	}
	nElem := len(src) / typeSize
	if nElem%8 != 0 {
		return src
	}

	body := nElem * typeSize
	dst := make([]byte, len(src))
	if unshuffle {
		untransBitElem(src[:body], dst[:body], nElem, typeSize)
	} else {
		transBitElem(src[:body], dst[:body], nElem, typeSize)
	}
	copy(dst[body:], src[body:])
	return dst
}

func transBit8x8(x uint64) uint64 {
	t := (x ^ (x >> 7)) & 0x00AA00AA00AA00AA
	x = x ^ t ^ (t << 7)
	t = (x ^ (x >> 14)) & 0x0000CCCC0000CCCC
	x = x ^ t ^ (t << 14)
	t = (x ^ (x >> 28)) & 0x00000000F0F0F0F0
	x = x ^ t ^ (t << 28)
	return x
}

func transBitElem(in, out []byte, size, elemSize int) {
	tmp := make([]byte, len(in))
	shuffled := make([]byte, len(in))
	byteTranspose(in, shuffled, size, elemSize)
	transBitByte(shuffled, tmp, size, elemSize)
	transBitRowEight(tmp, out, size, elemSize)
}

func untransBitElem(in, out []byte, size, elemSize int) {
	tmp := make([]byte, len(in))
	transByteBitRow(in, tmp, size, elemSize)
	shuffleBitEightElem(tmp, out, size, elemSize)
}

func byteTranspose(in, out []byte, size, elemSize int) {
	for i := 0; i < size; i++ {
		for j := 0; j < elemSize; j++ {
			out[j*size+i] = in[i*elemSize+j]
		}
	}
}

func transBitByte(in, out []byte, size, elemSize int) {
	nbyteBitrow := (elemSize * size) / 8
	for ii := 0; ii < nbyteBitrow; ii++ {
		x := transBit8x8(binary.LittleEndian.Uint64(in[ii*8:]))
		for kk := 0; kk < 8; kk++ {
			out[kk*nbyteBitrow+ii] = byte(x)
			x >>= 8
		}
	}
}

func transBitRowEight(in, out []byte, size, elemSize int) {
	row := size / 8
	for ii := 0; ii < 8; ii++ {
		for jj := 0; jj < elemSize; jj++ {
			src := (ii*elemSize + jj) * row
			dst := (jj*8 + ii) * row
			copy(out[dst:dst+row], in[src:src+row])
		}
	}
}

func transByteBitRow(in, out []byte, size, elemSize int) {
	nbyteRow := size / 8
	for jj := 0; jj < elemSize; jj++ {
		for ii := 0; ii < nbyteRow; ii++ {
			for kk := 0; kk < 8; kk++ {
				out[ii*8*elemSize+jj*8+kk] = in[(jj*8+kk)*nbyteRow+ii]
			}
		}
	}
}

func shuffleBitEightElem(in, out []byte, size, elemSize int) {
	nbyte := elemSize * size
	group := 8 * elemSize
	for jj := 0; jj < group; jj += 8 {
		for ii := 0; ii+group-1 < nbyte; ii += group {
			x := transBit8x8(binary.LittleEndian.Uint64(in[ii+jj:]))
			for kk := 0; kk < 8; kk++ {
				out[ii+jj/8+kk*elemSize] = byte(x)
				x >>= 8
			}
		}
	}
}
