package dfjoin

import "hash/crc32"

const AdlerBase = 65521

var x2nTable = [32]uint32{
	0x40000000, 0x20000000, 0x08000000, 0x00800000, 0x00008000,
	0xedb88320, 0xb1e6b092, 0xa06a2517, 0xed627dae, 0x88d14467,
	0xd7bbfe6a, 0xec447f11, 0x8e7ea170, 0x6427800e, 0x4d47bae0,
	0x09fe548f, 0x83852d0f, 0x30362f1a, 0x7b5a9cc3, 0x31fec169,
	0x9fec022a, 0x6c8dedc4, 0x15d6874d, 0x5fde7a4e, 0xbad90e37,
	0x2e4e5eef, 0x4eaba214, 0xa8a472c0, 0x429a969e, 0x148d302a,
	0xc40ba6d0, 0xc4e22c3c,
}

func IEEECrc32Combine(crc1, crc2 uint32, crc2SourceLen int64) uint32 {
	return crc32MultiMod(crc32X2nMod(crc2SourceLen, 3), crc1) ^ (crc2 & 0xffffffff)
}

// crc32MultiMod Return a(x) multiplied by b(x) modulo p(x), where p(x) is the CRC polynomial,
// reflected. For speed, this requires that a not be zero.
func crc32MultiMod(a, b uint32) uint32 {
	m := uint32(1) << 31
	p := uint32(0)

	for {
		if a&m != 0 {
			p ^= b
			if a&(m-1) == 0 {
				break
			}
		}
		m >>= 1
		if b&1 != 0 {
			b = (b >> 1) ^ crc32.IEEE
		} else {
			b >>= 1
		}
	}
	return p
}

// crc32X2nMod Return x^(n * 2^k) modulo p(x)
func crc32X2nMod(n int64, k uint) uint32 {
	p := uint32(1) << 31
	for n != 0 {
		if n&1 != 0 {
			p = crc32MultiMod(x2nTable[k&31], p)
		}
		n >>= 1
		k++
	}
	return p
}

func Adler32Combine(adler1, adler2 uint32, adler2SourceLen int64) uint32 {
	adler2SourceLen %= AdlerBase
	sum1 := adler1 & 0xffff
	sum2 := uint32(adler2SourceLen) * sum1
	sum2 %= AdlerBase
	sum1 += (adler2 & 0xffff) + AdlerBase - 1
	sum2 += ((adler1 >> 16) & 0xffff) + ((adler2 >> 16) & 0xffff) + AdlerBase - uint32(adler2SourceLen)

	if sum1 >= AdlerBase {
		sum1 -= AdlerBase
	}
	if sum1 >= AdlerBase {
		sum1 -= AdlerBase
	}
	if sum2 >= (uint32(AdlerBase) << 1) {
		sum2 -= uint32(AdlerBase) << 1
	}
	if sum2 >= AdlerBase {
		sum2 -= AdlerBase
	}

	return (sum2 << 16) | sum1
}
