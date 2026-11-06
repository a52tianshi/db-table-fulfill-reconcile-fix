package store

import (
	"fmt"
	"math/rand"
	"sort"
)

// Plan decides which new-table shard each order lands in, so the test can
// produce uneven data: empty shards, shards smaller than one scan page, exact
// page boundaries, and a hot shard. user_id = shard + 100*k keeps routing exact.
type Plan struct {
	Name       string
	InitCounts [Shards]int // old-table rows per target shard at init
	// Writer inserts go only to these shards (weighted). Frozen shards still
	// receive updates (row count unchanged) but never inserts.
	WriterShards  []int
	WriterWeight  []int
	UsersPerShard int64
	Notes         map[int]string
}

// Shard groups of the "skew" plan.
const (
	hotShard = 17
)

var (
	alwaysEmpty = []int{0, 1, 2, 3, 4} // never any row
	lateFill    = []int{5, 6, 7, 8, 9} // empty at init, writer inserts later
	edgeSizes   = map[int]int{         // frozen sizes around page=1000
		10: 1, 11: 2, 12: 999, 13: 1000, 14: 1001, 15: 1999, 16: 2000, 18: 2001,
	}
)

func NewPlan(name string, rows int, users int64) (*Plan, error) {
	p := &Plan{Name: name, UsersPerShard: max(users/Shards, 1), Notes: map[int]string{}}
	switch name {
	case "uniform":
		for i := 0; i < Shards; i++ {
			p.InitCounts[i] = rows / Shards
			if i < rows%Shards {
				p.InitCounts[i]++
			}
			p.WriterShards = append(p.WriterShards, i)
			p.WriterWeight = append(p.WriterWeight, 1)
		}
		return p, nil
	case "skew":
	default:
		return nil, fmt.Errorf("unknown dist %q (uniform|skew)", name)
	}
	special := map[int]bool{hotShard: true}
	for _, s := range alwaysEmpty {
		special[s] = true
		p.Notes[s] = "always empty"
	}
	for _, s := range lateFill {
		special[s] = true
		p.Notes[s] = "empty at init, writer fills"
	}
	fixed := 0
	for s, n := range edgeSizes {
		special[s] = true
		p.InitCounts[s] = n
		p.Notes[s] = fmt.Sprintf("frozen %d rows", n)
		fixed += n
	}
	if rows < fixed*3 {
		return nil, fmt.Errorf("skew plan needs -rows >= %d", fixed*3)
	}
	rest := rows - fixed
	hot := rest * 3 / 10 // 30% of the remainder in one shard
	p.InitCounts[hotShard] = hot
	p.Notes[hotShard] = "hot shard (~30%)"
	var normal []int
	for i := 0; i < Shards; i++ {
		if !special[i] {
			normal = append(normal, i)
		}
	}
	left := rest - hot
	for j, s := range normal {
		p.InitCounts[s] = left / len(normal)
		if j < left%len(normal) {
			p.InitCounts[s]++
		}
	}
	for _, s := range append(append([]int{}, lateFill...), normal...) {
		p.WriterShards = append(p.WriterShards, s)
		p.WriterWeight = append(p.WriterWeight, 1)
	}
	p.WriterShards = append(p.WriterShards, hotShard)
	p.WriterWeight = append(p.WriterWeight, len(normal)/2) // hot for inserts too
	return p, nil
}

// InitShards returns the shard of every init row, shuffled so order_id
// (assigned in this order) interleaves across shards.
func (p *Plan) InitShards(rng *rand.Rand) []int {
	var out []int
	for s, n := range p.InitCounts {
		for i := 0; i < n; i++ {
			out = append(out, s)
		}
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func (p *Plan) User(rng *rand.Rand, shard int) int64 {
	return int64(shard) + Shards*(1+rng.Int63n(p.UsersPerShard))
}

func (p *Plan) WriterUser(rng *rand.Rand) int64 {
	total := 0
	for _, w := range p.WriterWeight {
		total += w
	}
	x := rng.Intn(total)
	for i, w := range p.WriterWeight {
		if x < w {
			return p.User(rng, p.WriterShards[i])
		}
		x -= w
	}
	panic("unreachable")
}

// Frozen returns shards whose row count must not change after init.
func (p *Plan) Frozen() map[int]int {
	out := map[int]int{}
	if p.Name != "skew" {
		return out
	}
	for _, s := range alwaysEmpty {
		out[s] = 0
	}
	for s, n := range edgeSizes {
		out[s] = n
	}
	return out
}

func (p *Plan) Describe() string {
	keys := make([]int, 0, len(p.Notes))
	for s := range p.Notes {
		keys = append(keys, s)
	}
	sort.Ints(keys)
	out := fmt.Sprintf("dist=%s", p.Name)
	for _, s := range keys {
		out += fmt.Sprintf("\n  %s: %s", ShardTable(s), p.Notes[s])
	}
	return out
}
