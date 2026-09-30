package blosc

import (
	"encoding/binary"
	"fmt"
)

// BloscLZ is a FastLZ-derived codec. The bitstream follows c-blosc 1.21
// blosclz.c (scalar match finder). Compression may give up and return a nil
// buffer when the block is not worth encoding; the frame layer stores it raw.

const (
	blosclzMaxCopy        = 32
	blosclzMaxDistance    = 8191
	blosclzMaxFarDistance = 65535 + blosclzMaxDistance - 1
	blosclzHashLog        = 14
	blosclzHashLog2       = 12
)

var blosclzRatioMin = [10]float64{0, 2, 1.5, 1.2, 1.2, 1.2, 1.2, 1.15, 1.1, 1.0}

var blosclzHashLogs = [10]uint{0, blosclzHashLog - 2, blosclzHashLog - 1, blosclzHashLog, blosclzHashLog,
	blosclzHashLog, blosclzHashLog, blosclzHashLog, blosclzHashLog, blosclzHashLog}

type blosclzCodec struct{}

func (c *blosclzCodec) Name() string { return "blosclz" }

func (c *blosclzCodec) Compress(data []byte, level int) ([]byte, error) {
	return c.CompressSplit(data, level, true)
}

// CompressSplit honors c-blosc's split_block argument, which changes the
// minimum match length. split is true when the chunk's dont_split flag is clear.
func (c *blosclzCodec) CompressSplit(data []byte, level int, split bool) ([]byte, error) {
	out := blosclzCompress(data, level, split)
	if out == nil {
		return nil, nil
	}
	return out, nil
}

func (c *blosclzCodec) Decompress(data []byte, expectedSize int) ([]byte, error) {
	if expectedSize < 0 {
		return nil, fmt.Errorf("blosclz: negative size")
	}
	dst := make([]byte, expectedSize)
	n, ok := blosclzDecompress(data, dst)
	if !ok || n != expectedSize {
		return nil, fmt.Errorf("blosclz: decompress failed")
	}
	return dst, nil
}

func blosclzHash(seq uint32, hashlog uint) uint32 {
	return (seq * 2654435761) >> (32 - hashlog)
}

func blosclzCompress(input []byte, clevel int, split bool) []byte {
	if clevel < 1 {
		clevel = 1
	}
	if clevel > 9 {
		clevel = 9
	}
	length := len(input)
	if length < 16 || length < 66 {
		return nil
	}

	maxlen := length / 4
	shift := length - maxlen
	cratio := blosclzCRatio(input[shift:shift+maxlen], 3, 3)
	if cratio < blosclzRatioMin[clevel] {
		return nil
	}

	ipshift := 4
	minlen := 4
	if !split || cratio < 4 {
		ipshift = 3
		minlen = 3
	}
	hashlog := blosclzHashLogs[clevel]

	out := make([]byte, length)
	ip := 0
	ipBound := length - 1
	ipLimit := length - 12
	op := 0
	htab := make([]uint32, 1<<blosclzHashLog)

	copyN := 4
	out[op] = blosclzMaxCopy - 1
	op++
	copy(out[op:op+4], input[ip:ip+4])
	op += 4
	ip += 4

	for ip < ipLimit {
		anchor := ip
		seq := binary.LittleEndian.Uint32(input[ip:])
		hval := blosclzHash(seq, hashlog)
		ref := int(htab[hval])
		distance := anchor - ref
		htab[hval] = uint32(anchor)

		if distance == 0 || distance >= blosclzMaxFarDistance {
			if !blosclzLiteral(&ip, &op, &copyN, out, input, anchor) {
				return nil
			}
			continue
		}
		if binary.LittleEndian.Uint32(input[ref:]) != binary.LittleEndian.Uint32(input[ip:]) {
			if !blosclzLiteral(&ip, &op, &copyN, out, input, anchor) {
				return nil
			}
			continue
		}
		ref += 4
		ip = anchor + 4
		distance--
		if distance == 0 {
			ip = blosclzGetRun(input, ip, ipBound, ref)
		} else {
			ip = blosclzGetMatch(input, ip, ipBound, ref)
		}
		ip -= ipshift
		matchLen := ip - anchor
		if matchLen < minlen || (matchLen <= 5 && distance >= blosclzMaxDistance) {
			if !blosclzLiteral(&ip, &op, &copyN, out, input, anchor) {
				return nil
			}
			continue
		}

		if copyN > 0 {
			out[op-copyN-1] = byte(copyN - 1)
		} else {
			op--
		}
		copyN = 0

		if distance < blosclzMaxDistance {
			if matchLen < 7 {
				if !blosclzMatchShort(&op, out, matchLen, distance) {
					return nil
				}
			} else {
				if !blosclzMatchLong(&op, out, matchLen, distance) {
					return nil
				}
			}
		} else {
			distance -= blosclzMaxDistance
			if matchLen < 7 {
				if !blosclzMatchShortFar(&op, out, matchLen, distance) {
					return nil
				}
			} else {
				if !blosclzMatchLongFar(&op, out, matchLen, distance) {
					return nil
				}
			}
		}

		if ip+4 <= length {
			seq = binary.LittleEndian.Uint32(input[ip:])
			hval = blosclzHash(seq, hashlog)
			htab[hval] = uint32(ip)
			ip++
			if clevel == 9 {
				seq >>= 8
				hval = blosclzHash(seq, hashlog)
				htab[hval] = uint32(ip)
				ip++
			} else {
				ip++
			}
		} else {
			ip += 2
		}
		if op+1 > len(out) {
			return nil
		}
		out[op] = blosclzMaxCopy - 1
		op++
	}

	for ip <= ipBound {
		if op+2 > len(out) {
			return nil
		}
		out[op] = input[ip]
		op++
		ip++
		copyN++
		if copyN == blosclzMaxCopy {
			copyN = 0
			out[op] = blosclzMaxCopy - 1
			op++
		}
	}
	if copyN > 0 {
		out[op-copyN-1] = byte(copyN - 1)
	} else {
		op--
	}
	out[0] |= 1 << 5
	return out[:op]
}

func blosclzLiteral(ip, op, copyN *int, out, input []byte, anchor int) bool {
	if *op+2 > len(out) {
		return false
	}
	out[*op] = input[anchor]
	*op++
	anchor++
	*ip = anchor
	*copyN++
	if *copyN == blosclzMaxCopy {
		*copyN = 0
		out[*op] = blosclzMaxCopy - 1
		*op++
	}
	return true
}

func blosclzMatchShort(op *int, out []byte, matchLen, distance int) bool {
	if *op+2 > len(out) {
		return false
	}
	out[*op] = byte((matchLen << 5) + (distance >> 8))
	*op++
	out[*op] = byte(distance & 255)
	*op++
	return true
}

func blosclzMatchLong(op *int, out []byte, matchLen, distance int) bool {
	if *op+1 > len(out) {
		return false
	}
	out[*op] = byte((7 << 5) + (distance >> 8))
	*op++
	matchLen -= 7
	for matchLen >= 255 {
		if *op+1 > len(out) {
			return false
		}
		out[*op] = 255
		*op++
		matchLen -= 255
	}
	if *op+2 > len(out) {
		return false
	}
	out[*op] = byte(matchLen)
	*op++
	out[*op] = byte(distance & 255)
	*op++
	return true
}

func blosclzMatchShortFar(op *int, out []byte, matchLen, distance int) bool {
	if *op+4 > len(out) {
		return false
	}
	out[*op] = byte((matchLen << 5) + 31)
	*op++
	out[*op] = 255
	*op++
	out[*op] = byte(distance >> 8)
	*op++
	out[*op] = byte(distance & 255)
	*op++
	return true
}

func blosclzMatchLongFar(op *int, out []byte, matchLen, distance int) bool {
	if *op+1 > len(out) {
		return false
	}
	out[*op] = byte((7 << 5) + 31)
	*op++
	matchLen -= 7
	for matchLen >= 255 {
		if *op+1 > len(out) {
			return false
		}
		out[*op] = 255
		*op++
		matchLen -= 255
	}
	if *op+4 > len(out) {
		return false
	}
	out[*op] = byte(matchLen)
	*op++
	out[*op] = 255
	*op++
	out[*op] = byte(distance >> 8)
	*op++
	out[*op] = byte(distance & 255)
	*op++
	return true
}

func blosclzGetRun(in []byte, ip, ipBound, ref int) int {
	x := in[ip-1]
	for ip < ipBound-8 {
		if ref+8 > len(in) || ip+8 > len(in) {
			break
		}
		if binary.LittleEndian.Uint64(in[ref:]) != blosclzBroadcast(x) {
			for ref < len(in) && ip < len(in) {
				rv := in[ref]
				ref++
				if rv != x {
					return ip
				}
				ip++
			}
			return ip
		}
		ip += 8
		ref += 8
	}
	for ip < ipBound && ref < len(in) && in[ref] == x {
		ref++
		ip++
	}
	return ip
}

func blosclzGetMatch(in []byte, ip, ipBound, ref int) int {
	for ip < ipBound-8 {
		if ref+8 > len(in) || ip+8 > len(in) {
			break
		}
		if binary.LittleEndian.Uint64(in[ref:]) != binary.LittleEndian.Uint64(in[ip:]) {
			for ref < len(in) && ip < len(in) {
				rv, iv := in[ref], in[ip]
				ref++
				ip++
				if rv != iv {
					return ip
				}
			}
			return ip
		}
		ip += 8
		ref += 8
	}
	for ip < ipBound && ref < len(in) && ip < len(in) {
		rv, iv := in[ref], in[ip]
		ref++
		ip++
		if rv != iv {
			return ip
		}
	}
	return ip
}

func blosclzBroadcast(x byte) uint64 {
	v := uint64(x)
	v |= v << 8
	v |= v << 16
	v |= v << 32
	return v
}

func blosclzCRatio(in []byte, minlen, ipshift int) float64 {
	limit := len(in)
	hashlen := 1 << blosclzHashLog2
	if limit > hashlen {
		limit = hashlen
	}
	if limit < 12 {
		return 0
	}
	htab := make([]uint16, hashlen)
	ip := 0
	ipBound := limit - 1
	ipLimit := limit - 12
	oc := 5
	copyN := 4

	for ip < ipLimit {
		anchor := ip
		seq := binary.LittleEndian.Uint32(in[ip:])
		hval := blosclzHash(seq, blosclzHashLog2)
		ref := int(htab[hval])
		distance := anchor - ref
		htab[hval] = uint16(anchor)

		if distance == 0 || distance >= blosclzMaxFarDistance {
			oc++
			anchor++
			ip = anchor
			copyN++
			if copyN == blosclzMaxCopy {
				copyN = 0
				oc++
			}
			continue
		}
		if ip+4 > len(in) || ref+4 > len(in) || binary.LittleEndian.Uint32(in[ref:]) != binary.LittleEndian.Uint32(in[ip:]) {
			oc++
			anchor++
			ip = anchor
			copyN++
			if copyN == blosclzMaxCopy {
				copyN = 0
				oc++
			}
			continue
		}
		ref += 4
		ip = anchor + 4
		distance--
		if distance == 0 {
			ip = blosclzGetRun(in, ip, ipBound, ref)
		} else {
			ip = blosclzGetMatch(in, ip, ipBound, ref)
		}
		ip -= ipshift
		matchLen := ip - anchor
		if matchLen < minlen {
			oc++
			anchor++
			ip = anchor
			copyN++
			if copyN == blosclzMaxCopy {
				copyN = 0
				oc++
			}
			continue
		}
		if copyN == 0 {
			oc--
		}
		copyN = 0
		if distance < blosclzMaxDistance {
			if matchLen >= 7 {
				oc += (matchLen-7)/255 + 1
			}
			oc += 2
		} else {
			if matchLen >= 7 {
				oc += (matchLen-7)/255 + 1
			}
			oc += 4
		}
		if ip+4 <= len(in) {
			seq = binary.LittleEndian.Uint32(in[ip:])
			hval = blosclzHash(seq, blosclzHashLog2)
			htab[hval] = uint16(ip)
		}
		ip += 2
		oc++
	}
	if oc <= 0 {
		return 0
	}
	return float64(ip) / float64(oc)
}

func blosclzDecompress(src, dst []byte) (int, bool) {
	if len(src) == 0 {
		return 0, false
	}
	ip := 0
	op := 0
	ipLimit := len(src)
	ctrl := uint32(src[ip]) & 31
	ip++

	for {
		if ctrl >= 32 {
			matchLen := int(ctrl>>5) - 1
			ofs := int(ctrl&31) << 8
			ref := op - ofs
			var code byte
			if matchLen == 7-1 {
				for {
					if ip+1 >= ipLimit {
						return 0, false
					}
					code = src[ip]
					ip++
					matchLen += int(code)
					if code != 255 {
						break
					}
				}
			} else if ip+1 >= ipLimit {
				return 0, false
			}
			code = src[ip]
			ip++
			matchLen += 3
			ref -= int(code)
			if code == 255 && ofs == 31<<8 {
				if ip+1 >= ipLimit {
					return 0, false
				}
				ofs = int(src[ip]) << 8
				ip++
				ofs += int(src[ip])
				ip++
				ref = op - ofs - blosclzMaxDistance
			}
			if matchLen < 0 || op+matchLen > len(dst) {
				return 0, false
			}
			if ref < 1 {
				return 0, false
			}
			if ip >= ipLimit {
				break
			}
			ctrl = uint32(src[ip])
			ip++
			ref--
			if ref == op-1 {
				fill := dst[ref]
				for i := 0; i < matchLen; i++ {
					dst[op+i] = fill
				}
				op += matchLen
			} else {
				for i := 0; i < matchLen; i++ {
					dst[op+i] = dst[ref+i]
				}
				op += matchLen
			}
		} else {
			ctrl++
			if op+int(ctrl) > len(dst) || ip+int(ctrl) > ipLimit {
				return 0, false
			}
			copy(dst[op:op+int(ctrl)], src[ip:ip+int(ctrl)])
			op += int(ctrl)
			ip += int(ctrl)
			if ip >= ipLimit {
				break
			}
			ctrl = uint32(src[ip])
			ip++
		}
	}
	return op, true
}
