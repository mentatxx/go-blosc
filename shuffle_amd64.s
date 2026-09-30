//go:build amd64

#include "textflag.h"

// func hasAVX2() bool
TEXT ·hasAVX2(SB), NOSPLIT, $0-1
    // Check if CPUID is supported and get feature flags
    MOVL    $7, AX          // CPUID function 7: Extended Features
    XORL    CX, CX          // ECX = 0 (sub-leaf 0)
    CPUID
    // AVX2 is bit 5 of EBX
    ANDL    $0x20, BX       // Mask bit 5
    SETNE   AL              // AL = 1 if AVX2 supported
    MOVB    AL, ret+0(FP)
    RET

// Shuffle mask for typeSize=4: extracts byte 0 from each 4-byte element
// Input ymm:  [a0 a1 a2 a3 | b0 b1 b2 b3 | c0 c1 c2 c3 | d0 d1 d2 d3 | e0 e1 e2 e3 | f0 f1 f2 f3 | g0 g1 g2 g3 | h0 h1 h2 h3]
// We need to gather bytes at positions 0,4,8,12 from each 128-bit lane

// For typeSize=4 shuffle, we use a different approach:
// 1. Load 32 bytes (8 float32 elements)
// 2. Use VPSHUFB to rearrange bytes within each 128-bit lane
// 3. Use VPERMD to move dwords across lanes

DATA shuffle4_shuf<>+0(SB)/8, $0x0c080400ffffffff  // Gather bytes 0,4,8,12 -> low dword, high dword unused
DATA shuffle4_shuf<>+8(SB)/8, $0xffffffff0d090501  // Gather bytes 1,5,9,13 -> low dword of next position
DATA shuffle4_shuf<>+16(SB)/8, $0x0e0a0602ffffffff // Gather bytes 2,6,10,14
DATA shuffle4_shuf<>+24(SB)/8, $0xffffffff0f0b0703 // Gather bytes 3,7,11,15
GLOBL shuffle4_shuf<>(SB), RODATA, $32

// Permutation to move dwords after vpshufb
// After vpshufb each lane has: [byte0s, byte1s, byte2s, byte3s] as dwords
// We want: [all byte0s, all byte1s, all byte2s, all byte3s]
DATA shuffle4_perm<>+0(SB)/4, $0   // dword 0 from lane 0 -> pos 0
DATA shuffle4_perm<>+4(SB)/4, $4   // dword 0 from lane 1 -> pos 1
DATA shuffle4_perm<>+8(SB)/4, $1   // dword 1 from lane 0 -> pos 2
DATA shuffle4_perm<>+12(SB)/4, $5  // dword 1 from lane 1 -> pos 3
DATA shuffle4_perm<>+16(SB)/4, $2  // dword 2 from lane 0 -> pos 4
DATA shuffle4_perm<>+20(SB)/4, $6  // dword 2 from lane 1 -> pos 5
DATA shuffle4_perm<>+24(SB)/4, $3  // dword 3 from lane 0 -> pos 6
DATA shuffle4_perm<>+28(SB)/4, $7  // dword 3 from lane 1 -> pos 7
GLOBL shuffle4_perm<>(SB), RODATA, $32

// Shuffle mask for typeSize=4: rearranges each 128-bit lane
// Each lane: [a0 a1 a2 a3 | b0 b1 b2 b3 | c0 c1 c2 c3 | d0 d1 d2 d3]
// After shuffle: [a0 b0 c0 d0 | a1 b1 c1 d1 | a2 b2 c2 d2 | a3 b3 c3 d3]
DATA shuffle4_lane<>+0(SB)/1, $0x00  // byte 0 of elem 0
DATA shuffle4_lane<>+1(SB)/1, $0x04  // byte 0 of elem 1
DATA shuffle4_lane<>+2(SB)/1, $0x08  // byte 0 of elem 2
DATA shuffle4_lane<>+3(SB)/1, $0x0c  // byte 0 of elem 3
DATA shuffle4_lane<>+4(SB)/1, $0x01  // byte 1 of elem 0
DATA shuffle4_lane<>+5(SB)/1, $0x05  // byte 1 of elem 1
DATA shuffle4_lane<>+6(SB)/1, $0x09  // byte 1 of elem 2
DATA shuffle4_lane<>+7(SB)/1, $0x0d  // byte 1 of elem 3
DATA shuffle4_lane<>+8(SB)/1, $0x02  // byte 2 of elem 0
DATA shuffle4_lane<>+9(SB)/1, $0x06  // byte 2 of elem 1
DATA shuffle4_lane<>+10(SB)/1, $0x0a // byte 2 of elem 2
DATA shuffle4_lane<>+11(SB)/1, $0x0e // byte 2 of elem 3
DATA shuffle4_lane<>+12(SB)/1, $0x03 // byte 3 of elem 0
DATA shuffle4_lane<>+13(SB)/1, $0x07 // byte 3 of elem 1
DATA shuffle4_lane<>+14(SB)/1, $0x0b // byte 3 of elem 2
DATA shuffle4_lane<>+15(SB)/1, $0x0f // byte 3 of elem 3
// Second 128-bit lane (same pattern)
DATA shuffle4_lane<>+16(SB)/1, $0x00
DATA shuffle4_lane<>+17(SB)/1, $0x04
DATA shuffle4_lane<>+18(SB)/1, $0x08
DATA shuffle4_lane<>+19(SB)/1, $0x0c
DATA shuffle4_lane<>+20(SB)/1, $0x01
DATA shuffle4_lane<>+21(SB)/1, $0x05
DATA shuffle4_lane<>+22(SB)/1, $0x09
DATA shuffle4_lane<>+23(SB)/1, $0x0d
DATA shuffle4_lane<>+24(SB)/1, $0x02
DATA shuffle4_lane<>+25(SB)/1, $0x06
DATA shuffle4_lane<>+26(SB)/1, $0x0a
DATA shuffle4_lane<>+27(SB)/1, $0x0e
DATA shuffle4_lane<>+28(SB)/1, $0x03
DATA shuffle4_lane<>+29(SB)/1, $0x07
DATA shuffle4_lane<>+30(SB)/1, $0x0b
DATA shuffle4_lane<>+31(SB)/1, $0x0f
GLOBL shuffle4_lane<>(SB), RODATA, $32

// Unshuffle mask for typeSize=4: reverse the lane shuffle
// Input: [a0 b0 c0 d0 | a1 b1 c1 d1 | a2 b2 c2 d2 | a3 b3 c3 d3]
// Output: [a0 a1 a2 a3 | b0 b1 b2 b3 | c0 c1 c2 c3 | d0 d1 d2 d3]
DATA unshuffle4_lane<>+0(SB)/1, $0x00  // byte 0 -> pos 0
DATA unshuffle4_lane<>+1(SB)/1, $0x04  // byte 1 -> pos 1
DATA unshuffle4_lane<>+2(SB)/1, $0x08  // byte 2 -> pos 2
DATA unshuffle4_lane<>+3(SB)/1, $0x0c  // byte 3 -> pos 3
DATA unshuffle4_lane<>+4(SB)/1, $0x01  // byte 4 -> pos 4
DATA unshuffle4_lane<>+5(SB)/1, $0x05  // byte 5 -> pos 5
DATA unshuffle4_lane<>+6(SB)/1, $0x09  // byte 6 -> pos 6
DATA unshuffle4_lane<>+7(SB)/1, $0x0d  // byte 7 -> pos 7
DATA unshuffle4_lane<>+8(SB)/1, $0x02  // byte 8 -> pos 8
DATA unshuffle4_lane<>+9(SB)/1, $0x06  // byte 9 -> pos 9
DATA unshuffle4_lane<>+10(SB)/1, $0x0a // byte 10 -> pos 10
DATA unshuffle4_lane<>+11(SB)/1, $0x0e // byte 11 -> pos 11
DATA unshuffle4_lane<>+12(SB)/1, $0x03 // byte 12 -> pos 12
DATA unshuffle4_lane<>+13(SB)/1, $0x07 // byte 13 -> pos 13
DATA unshuffle4_lane<>+14(SB)/1, $0x0b // byte 14 -> pos 14
DATA unshuffle4_lane<>+15(SB)/1, $0x0f // byte 15 -> pos 15
// Second 128-bit lane (same pattern)
DATA unshuffle4_lane<>+16(SB)/1, $0x00
DATA unshuffle4_lane<>+17(SB)/1, $0x04
DATA unshuffle4_lane<>+18(SB)/1, $0x08
DATA unshuffle4_lane<>+19(SB)/1, $0x0c
DATA unshuffle4_lane<>+20(SB)/1, $0x01
DATA unshuffle4_lane<>+21(SB)/1, $0x05
DATA unshuffle4_lane<>+22(SB)/1, $0x09
DATA unshuffle4_lane<>+23(SB)/1, $0x0d
DATA unshuffle4_lane<>+24(SB)/1, $0x02
DATA unshuffle4_lane<>+25(SB)/1, $0x06
DATA unshuffle4_lane<>+26(SB)/1, $0x0a
DATA unshuffle4_lane<>+27(SB)/1, $0x0e
DATA unshuffle4_lane<>+28(SB)/1, $0x03
DATA unshuffle4_lane<>+29(SB)/1, $0x07
DATA unshuffle4_lane<>+30(SB)/1, $0x0b
DATA unshuffle4_lane<>+31(SB)/1, $0x0f
GLOBL unshuffle4_lane<>(SB), RODATA, $32

// Unshuffle permutation (reverse of shuffle4_perm)
DATA unshuffle4_perm<>+0(SB)/4, $0   // dword 0 -> pos 0
DATA unshuffle4_perm<>+4(SB)/4, $2   // dword 2 -> pos 1
DATA unshuffle4_perm<>+8(SB)/4, $4   // dword 4 -> pos 2
DATA unshuffle4_perm<>+12(SB)/4, $6  // dword 6 -> pos 3
DATA unshuffle4_perm<>+16(SB)/4, $1  // dword 1 -> pos 4
DATA unshuffle4_perm<>+20(SB)/4, $3  // dword 3 -> pos 5
DATA unshuffle4_perm<>+24(SB)/4, $5  // dword 5 -> pos 6
DATA unshuffle4_perm<>+28(SB)/4, $7  // dword 7 -> pos 7
GLOBL unshuffle4_perm<>(SB), RODATA, $32

// func shuffleBytesAVX2(dst, src []byte, typeSize int) bool
// Arguments:
//   dst: slice at 0(FP), 24 bytes (ptr, len, cap)
//   src: slice at 24(FP), 24 bytes (ptr, len, cap)
//   typeSize: int at 48(FP), 8 bytes
//   ret: bool at 56(FP), 1 byte
TEXT ·shuffleBytesAVX2(SB), NOSPLIT, $0-57
    MOVQ    dst_base+0(FP), DI      // dst pointer
    MOVQ    dst_len+8(FP), R8       // dst length (n)
    MOVQ    src_base+24(FP), SI     // src pointer
    MOVQ    src_len+32(FP), R9      // src length
    MOVQ    typeSize+48(FP), DX     // typeSize

    // Check if we can use AVX2: need typeSize == 4 and at least 32 bytes
    CMPQ    DX, $4
    JNE     fallback
    CMPQ    R8, $32
    JL      fallback

    // Calculate number of elements and number of 32-byte chunks
    MOVQ    R8, AX
    SHRQ    $2, AX                  // numElements = n / 4
    MOVQ    AX, R10                 // R10 = numElements

    // For shuffle output, we need to write to 4 separate regions:
    // - bytes 0 of each element go to dst[0:numElements]
    // - bytes 1 of each element go to dst[numElements:2*numElements]
    // - bytes 2 of each element go to dst[2*numElements:3*numElements]
    // - bytes 3 of each element go to dst[3*numElements:4*numElements]

    // Process in chunks of 8 elements (32 bytes input -> 32 bytes output scattered)
    MOVQ    R10, CX
    SHRQ    $3, CX                  // numChunks = numElements / 8
    JZ      fallback                // If no full chunks, fallback

    // Load shuffle masks
    VMOVDQU shuffle4_lane<>(SB), Y2   // Lane shuffle mask
    VMOVDQU shuffle4_perm<>(SB), Y3   // Cross-lane permutation

    // Calculate destination offsets
    // R11 = dst + 0 (byte 0 output)
    // R12 = dst + numElements (byte 1 output)
    // R13 = dst + 2*numElements (byte 2 output)
    // R14 = dst + 3*numElements (byte 3 output)
    MOVQ    DI, R11
    LEAQ    (DI)(R10*1), R12
    LEAQ    (DI)(R10*2), R13
    LEAQ    (R12)(R10*2), R14

    XORQ    BX, BX                  // chunk counter

shuffle_loop:
    // Load 32 bytes (8 elements)
    VMOVDQU (SI), Y0

    // Step 1: Shuffle within each 128-bit lane
    // Each lane: [a0 a1 a2 a3 | b0 b1 b2 b3 | c0 c1 c2 c3 | d0 d1 d2 d3]
    // becomes:   [a0 b0 c0 d0 | a1 b1 c1 d1 | a2 b2 c2 d2 | a3 b3 c3 d3]
    VPSHUFB Y2, Y0, Y1

    // Step 2: Permute dwords across lanes
    // After vpshufb, each lane has the bytes grouped by position within element
    // Use vpermd to interleave the lanes properly
    VPERMD  Y1, Y3, Y0

    // Now Y0 contains:
    // [a0 b0 c0 d0 e0 f0 g0 h0 | a1 b1 c1 d1 e1 f1 g1 h1 | a2 b2 c2 d2 e2 f2 g2 h2 | a3 b3 c3 d3 e3 f3 g3 h3]
    // as 4 qwords, each containing 8 bytes for one byte position

    // Extract and store each 8-byte group to its destination
    // Byte position 0: first 8 bytes of Y0
    VMOVQ   X0, (R11)

    // Byte position 1: bytes 8-15 of Y0
    VPSRLDQ $8, X0, X1
    VMOVQ   X1, (R12)

    // Byte position 2: extract high 128-bit lane, first 8 bytes
    VEXTRACTI128 $1, Y0, X1
    VMOVQ   X1, (R13)

    // Byte position 3: high 128-bit lane, bytes 8-15
    VPSRLDQ $8, X1, X1
    VMOVQ   X1, (R14)

    // Advance pointers
    ADDQ    $32, SI                 // src += 32
    ADDQ    $8, R11                 // dst byte 0 region += 8
    ADDQ    $8, R12                 // dst byte 1 region += 8
    ADDQ    $8, R13                 // dst byte 2 region += 8
    ADDQ    $8, R14                 // dst byte 3 region += 8

    INCQ    BX
    CMPQ    BX, CX
    JL      shuffle_loop

    // Handle remaining elements (< 8) with scalar code
    // Calculate how many elements were processed
    MOVQ    CX, AX
    SHLQ    $3, AX                  // processed elements = chunks * 8

    // If there are remaining elements, we need to handle them
    // For simplicity, we'll signal success and let Go handle the remainder
    // The caller should check the return value and process remaining bytes

    VZEROUPPER
    MOVB    $1, ret+56(FP)
    RET

fallback:
    MOVB    $0, ret+56(FP)
    RET

// func unshuffleBytesAVX2(dst, src []byte, typeSize int) bool
TEXT ·unshuffleBytesAVX2(SB), NOSPLIT, $0-57
    MOVQ    dst_base+0(FP), DI      // dst pointer
    MOVQ    dst_len+8(FP), R8       // dst length (n)
    MOVQ    src_base+24(FP), SI     // src pointer
    MOVQ    src_len+32(FP), R9      // src length
    MOVQ    typeSize+48(FP), DX     // typeSize

    // Check if we can use AVX2: need typeSize == 4 and at least 32 bytes
    CMPQ    DX, $4
    JNE     unshuffle_fallback
    CMPQ    R8, $32
    JL      unshuffle_fallback

    // Calculate number of elements
    MOVQ    R8, AX
    SHRQ    $2, AX                  // numElements = n / 4
    MOVQ    AX, R10                 // R10 = numElements

    // Process in chunks of 8 elements (32 bytes)
    MOVQ    R10, CX
    SHRQ    $3, CX                  // numChunks = numElements / 8
    JZ      unshuffle_fallback

    // Load unshuffle masks
    VMOVDQU unshuffle4_lane<>(SB), Y2   // Lane unshuffle mask
    VMOVDQU unshuffle4_perm<>(SB), Y3   // Cross-lane permutation

    // Calculate source offsets (reading from scattered locations)
    // R11 = src + 0 (byte 0 input)
    // R12 = src + numElements (byte 1 input)
    // R13 = src + 2*numElements (byte 2 input)
    // R14 = src + 3*numElements (byte 3 input)
    MOVQ    SI, R11
    LEAQ    (SI)(R10*1), R12
    LEAQ    (SI)(R10*2), R13
    LEAQ    (R12)(R10*2), R14

    XORQ    BX, BX                  // chunk counter

unshuffle_loop:
    // Load 8 bytes from each byte-position region
    VMOVQ   (R11), X0               // bytes 0: a0 b0 c0 d0 e0 f0 g0 h0
    VMOVQ   (R12), X1               // bytes 1: a1 b1 c1 d1 e1 f1 g1 h1
    VMOVQ   (R13), X4               // bytes 2: a2 b2 c2 d2 e2 f2 g2 h2
    VMOVQ   (R14), X5               // bytes 3: a3 b3 c3 d3 e3 f3 g3 h3

    // Combine into a single YMM register
    // We need: [a0 b0 c0 d0 e0 f0 g0 h0 | a1 b1 c1 d1 e1 f1 g1 h1 | a2 b2 c2 d2 e2 f2 g2 h2 | a3 b3 c3 d3 e3 f3 g3 h3]

    // First, combine X0 and X1 into low 128 bits
    VPUNPCKLQDQ X1, X0, X0          // X0 = [byte0s | byte1s]

    // Combine X4 and X5 into another 128-bit register
    VPUNPCKLQDQ X5, X4, X4          // X4 = [byte2s | byte3s]

    // Combine into YMM
    VINSERTI128 $1, X4, Y0, Y0      // Y0 = [byte0s | byte1s | byte2s | byte3s]

    // Step 1: Permute dwords to reverse the interleaving
    VPERMD  Y0, Y3, Y1

    // Step 2: Unshuffle within each lane
    VPSHUFB Y2, Y1, Y0

    // Store 32 bytes (8 elements in original order)
    VMOVDQU Y0, (DI)

    // Advance pointers
    ADDQ    $32, DI                 // dst += 32
    ADDQ    $8, R11                 // src byte 0 region += 8
    ADDQ    $8, R12                 // src byte 1 region += 8
    ADDQ    $8, R13                 // src byte 2 region += 8
    ADDQ    $8, R14                 // src byte 3 region += 8

    INCQ    BX
    CMPQ    BX, CX
    JL      unshuffle_loop

    VZEROUPPER
    MOVB    $1, ret+56(FP)
    RET

unshuffle_fallback:
    MOVB    $0, ret+56(FP)
    RET

