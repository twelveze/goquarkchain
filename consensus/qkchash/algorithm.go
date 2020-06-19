//+build !nt

package qkchash

import (
	"encoding/binary"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	accessRound     = 64
	cacheEntryCnt   = 1024 * 64
	cacheAheadRound = 64 // 64*30000
)

var (
	EpochLength = uint64(30000) //blocks pre epoch
)

type cacheSeed struct {
	mu     sync.RWMutex
	caches []qkcCache
}

func NewcacheSeed() *cacheSeed {
	firstCache := generateCache(cacheEntryCnt, common.Hash{}.Bytes())
	caches := make([]qkcCache, 0)
	caches = append(caches, firstCache)
	return &cacheSeed{
		caches: caches,
	}
}

func (c *cacheSeed) getCacheFromHeight(block uint64) qkcCache {
	epoch := int(block / EpochLength)
	lenCaches := len(c.caches)
	if epoch < lenCaches {
		return c.caches[epoch]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	needAddCnt := epoch - lenCaches + cacheAheadRound
	seed := c.caches[len(c.caches)-1].seed
	for i := 0; i < needAddCnt; i++ {
		seed = crypto.Keccak256(seed)
		c.caches = append(c.caches, generateCache(cacheEntryCnt, seed))
	}
	return c.caches[epoch]
}

// qkcCache is the union type of cache for qkchash algo.
// Note in Go impl, `nativeCache` will be empty.
type qkcCache struct {
	ls   []uint64
	set  map[uint64]struct{}
	seed []byte
}

// fnv64 is an algorithm inspired by the FNV hash, which in some cases is used as
// a non-associative substitute for XOR. This is 64-bit version.
func fnv64(a, b uint64) uint64 {
	return a*0x100000001b3 ^ b
}

// generateCache generates cache for qkchash. Will also generate underlying cache
// in native c++ impl if needed.
func generateCache(cnt int, seed []byte) qkcCache {
	ls := []uint64{}
	set := make(map[uint64]struct{})
	for i := uint32(0); i < uint32(cnt/8); i++ {
		iBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(iBytes, i)
		bs := crypto.Keccak512(append(seed, iBytes...))
		// Read 8 bytes as uint64
		for j := 0; j < len(bs); j += 8 {
			ele := binary.LittleEndian.Uint64(bs[j:])
			if _, ok := set[ele]; !ok {
				ls = append(ls, ele)
				set[ele] = struct{}{}
			}
		}
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i] < ls[j] })
	return qkcCache{ls, set, seed}
}

// qkcHashGo is the Go implementation.
func qkcHashX(seed []byte, cache qkcCache, useX bool) (digest []byte, result []byte, err error) {
	const mixBytes = 128
	// Copy the cache since modification is needed
	tree := NewLLRB()
	for _, v := range cache.ls {
		tree.ReplaceOrInsert(v)
	}

	seed = crypto.Keccak512(seed)
	seedHead := binary.LittleEndian.Uint64(seed)

	// Start the mix with replicated seed
	mix := make([]uint64, mixBytes/8)
	for i := 0; i < len(mix); i++ {
		mix[i] = binary.LittleEndian.Uint64(seed[i%8*8:])
	}

	// TODO: can be improved using balanced tree
	for i := 0; i < accessRound; i++ {
		newData := make([]uint64, mixBytes/8)
		p := fnv64(uint64(i)^seedHead, mix[i%len(mix)])
		for j := 0; j < len(mix); j++ {
			idx := p % uint64(tree.Len())

			v := tree.DeleteAt(int(idx))
			newData[j] = v

			// Generate a random item and insert
			p = fnv64(p, v)
			tree.ReplaceOrInsert(p)
			// Continue next search
			p = fnv64(p, v)
		}

		for j := 0; j < len(mix); j++ {
			mix[j] = fnv64(mix[j], newData[j])
		}
	}

	// Compress mix
	for i := 0; i < len(mix); i += 4 {
		mix[i/4] = fnv64(fnv64(fnv64(mix[i], mix[i+1]), mix[i+2]), mix[i+3])
	}
	mix = mix[:len(mix)/4]

	if useX {
		stats := tree.GetRotationStats()
		for i := 0; i < len(stats); i++ {
			mix[i] ^= stats[i]
		}
	}

	digest = make([]byte, common.HashLength)
	for i, val := range mix {
		binary.LittleEndian.PutUint64(digest[i*8:], val)
	}
	result = crypto.Keccak256(append(seed, digest...))
	return digest, result, nil
}
