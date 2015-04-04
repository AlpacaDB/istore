package bitvector

import (
	"bytes"
	"errors"
	"sort"
)

type BitVector struct {
	size int
	bits []uint8
}

func New(size int) *BitVector {
	nbytes := size >> 3
	bits := make([]uint8, nbytes, nbytes)
	return &BitVector{
		size: size,
		bits: bits,
	}
}

func (bv *BitVector) Set(i uint) {
	bv.bits[i>>3] |= uint8(0x1) << (i & 0x7)
}

func (bv *BitVector) Unset(i uint) {
	bv.bits[i>>3] &= ^(uint8(0x1) << (i & 0x7))
}

func (bv *BitVector) Get(i uint) bool {
	return (bv.bits[i>>3] & (uint8(0x1) << (i & 0x7))) != 0
}

func (bv *BitVector) String() string {
	buf := new(bytes.Buffer)

	for i := 0; i < bv.size; i++ {
		if i > 0 && i%8 == 0 {
			buf.WriteByte(' ')
		}
		if bv.Get(uint(i)) {
			buf.WriteByte('1')
		} else {
			buf.WriteByte('0')
		}
	}

	return buf.String()
}

// Scan scans string and construct BitVector.  The allowed characters are
// either '0', '1', or white space (' ').  Otherwise, it returns error.
func Scan(s string) (*BitVector, error) {
	bits := make([]bool, 0, len(s))
	for _, c := range s {
		switch c {
		case '1':
			bits = append(bits, true)
		case '0':
			bits = append(bits, false)
		case ' ':
		default:
			return nil, errors.New("BitVector parse error")
		}
	}
	bv := New(len(bits))
	for i, b := range bits {
		if b {
			bv.Set(uint(i))
		}
	}

	return bv, nil
}

// MustScan scans the string and panics if there is error.
func MustScan(s string) *BitVector {
	bv, err := Scan(s)
	if err != nil {
		panic(err)
	}
	return bv
}

// Uint64 returns the integer value of the first 64 bits.
func (bv *BitVector) Uint64() uint64 {
	if bv.size <= 8 {
		return uint64(bv.bits[0])
	} else if bv.size <= 16 {
		return uint64((uint64(bv.bits[1]) << 8) | uint64(bv.bits[0]))
	} else if bv.size <= 24 {
		return uint64((uint64(bv.bits[2]) << 16) | (uint64(bv.bits[1]) << 8) | uint64(bv.bits[0]))
	}

	return uint64((uint64(bv.bits[3]) << 24) | (uint64(bv.bits[2]) << 16) | (uint64(bv.bits[1]) << 8) | uint64(bv.bits[0]))
}

func (bv *BitVector) ByteSize() int {
	return (bv.size+7) >> 3
}

// A slice of BitVector
type BitVectorSlice []*BitVector

func (s BitVectorSlice) Len() int {
	return len(s)
}

func (s BitVectorSlice) Less(i, j int) bool {
	for x := 0; x < s[i].ByteSize(); x++ {
		if s[i].bits[x] != s[j].bits[x] {
			return s[i].bits[x] < s[j].bits[x]
		}
	}
	return false
}

func (s BitVectorSlice) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

// Hamming calculates the hamming distance of two bit vectors.
func Hamming(x, y *BitVector) int {
	dist := 0

	for i := 0; i < x.ByteSize(); i++ {
		dist += int(popcnt[x.bits[i] ^ y.bits[i]])
	}

	return dist
}

// ByHamming embeds BitVectorSlice and extends Less()
type ByHamming struct {
	BitVectorSlice
	c *BitVector
}

// Less returns true if s[i] is closer to the center than s[j]
func (s *ByHamming) Less(i, j int) bool {
	dist_i := Hamming(s.c, s.BitVectorSlice[i])
	dist_j := Hamming(s.c, s.BitVectorSlice[j])
	return dist_i < dist_j
}

// SortFrom sorts s by the hamming distance from c
func (s BitVectorSlice) SortFrom(c *BitVector) {
	sorter := &ByHamming{s, c}
	sort.Sort(sorter)
}
