package process

import (
	"reflect"
	"testing"
)

func TestExpandPlacement_EmptyUsesFallbackOnDefault(t *testing.T) {
	got, err := ExpandPlacement(nil, []string{"local", "burst"}, 3, "local")
	if err != nil {
		t.Fatalf("ExpandPlacement: %v", err)
	}
	want := []TierAssignment{{0, "local"}, {1, "local"}, {2, "local"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandPlacement_MultiTierContiguousIndexesInOrder(t *testing.T) {
	got, err := ExpandPlacement(map[string]int{"local": 1, "burst": 2}, []string{"local", "burst"}, 99, "local")
	if err != nil {
		t.Fatalf("ExpandPlacement: %v", err)
	}
	want := []TierAssignment{{0, "local"}, {1, "burst"}, {2, "burst"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandPlacement_StableAcrossCalls(t *testing.T) {
	pm := map[string]int{"burst": 2, "local": 1}
	a, _ := ExpandPlacement(pm, []string{"local", "burst"}, 0, "local")
	b, _ := ExpandPlacement(pm, []string{"local", "burst"}, 0, "local")
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("non-deterministic: %v vs %v", a, b)
	}
}

func TestExpandPlacement_SkipsZeroCountTiers(t *testing.T) {
	got, _ := ExpandPlacement(map[string]int{"local": 0, "burst": 2}, []string{"local", "burst"}, 0, "local")
	want := []TierAssignment{{0, "burst"}, {1, "burst"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandPlacement_UnknownTierIsError(t *testing.T) {
	if _, err := ExpandPlacement(map[string]int{"ghost": 1}, []string{"local"}, 0, "local"); err == nil {
		t.Fatal("expected error for tier not in tierOrder")
	}
}

func TestExpandPlacement_NegativeCountIsError(t *testing.T) {
	if _, err := ExpandPlacement(map[string]int{"local": -1}, []string{"local"}, 0, "local"); err == nil {
		t.Fatal("expected error for negative count")
	}
}

func TestExpandPlacement_ZeroTotalIsError(t *testing.T) {
	if _, err := ExpandPlacement(map[string]int{"local": 0}, []string{"local"}, 0, "local"); err == nil {
		t.Fatal("expected error when total replicas resolves to zero")
	}
}
