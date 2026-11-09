package store

import (
	"math/rand"
	"reflect"
	"testing"
)

// Small quickstarts must use uniform: do not "fix" them by silently shrinking
// the skew dataset's page-boundary fixtures.
func TestQuickstartUniformAndSkewBoundary(t *testing.T) {
	if _, err := NewPlan("skew", 2000, 1000); err == nil {
		t.Fatal("small skew input must reject an incomplete boundary fixture")
	}
	p, err := NewPlan("uniform", 2000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	counts := map[int]int{}
	for _, shard := range p.InitShards(rng) {
		counts[shard]++
		if user := p.User(rng, shard); user%100 != int64(shard) {
			t.Fatalf("user %d routes outside shard %d", user, shard)
		}
	}
	for shard := 0; shard < 100; shard++ {
		if counts[shard] != 20 {
			t.Fatalf("shard %d: got %d rows, want 20", shard, counts[shard])
		}
	}
	if len(p.Frozen()) != 0 {
		t.Fatal("uniform quickstart must not claim skew boundary coverage")
	}
}

func TestDefaultSkewPreservesFrozenPageBoundaries(t *testing.T) {
	p, err := NewPlan("skew", 50000, 20000)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]int{0: 0, 1: 0, 2: 0, 3: 0, 4: 0,
		10: 1, 11: 2, 12: 999, 13: 1000, 14: 1001, 15: 1999, 16: 2000, 18: 2001}
	if !reflect.DeepEqual(p.Frozen(), want) {
		t.Fatalf("frozen counts=%v, want %v", p.Frozen(), want)
	}
	if rows := p.InitShards(rand.New(rand.NewSource(1))); len(rows) != 50000 {
		t.Fatalf("generated %d rows, want 50000", len(rows))
	}
}
