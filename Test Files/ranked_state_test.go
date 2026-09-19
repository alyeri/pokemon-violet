package main

import (
	"path/filepath"
	"testing"
)

func TestRankedConsensusPersistsAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranked-state.json")
	store := newRankedStore(path)
	users := map[int64]string{1: "u-loser", 2: "u-winner"}
	results := map[int64]int64{1: 0, 2: 1}
	applied, err := store.recordConsensus("session-1", "local-ranked-singles-1", users, results)
	if err != nil || !applied {
		t.Fatalf("result not applied: applied=%v err=%v", applied, err)
	}
	if applied, err = store.recordConsensus("session-1", "local-ranked-singles-1", users, results); err != nil || applied {
		t.Fatalf("duplicate result changed state: applied=%v err=%v", applied, err)
	}

	winner := store.stats("local-ranked-singles-1", "u-winner")
	loser := store.stats("local-ranked-singles-1", "u-loser")
	if winner.MatchCount != 1 || winner.WinCount != 1 || winner.LoseCount != 0 || winner.Rating != 1530 || winner.Point != 1 || winner.MaxPoint != 1 {
		t.Fatalf("unexpected winner state: %+v", winner)
	}
	if loser.MatchCount != 1 || loser.WinCount != 0 || loser.LoseCount != 1 || loser.Rating != 1470 {
		t.Fatalf("unexpected loser state: %+v", loser)
	}

	reloaded := newRankedStore(path)
	if got := reloaded.stats("local-ranked-singles-1", "u-winner"); got != winner {
		t.Fatalf("persisted winner mismatch: got=%+v want=%+v", got, winner)
	}
	if battles, disconnects := reloaded.battleStats("u-winner"); battles != 1 || disconnects != 0 {
		t.Fatalf("battle stats=%d/%d, want 1/0", battles, disconnects)
	}
}

func TestRankedConsensusRejectsContradictoryResults(t *testing.T) {
	store := newRankedStore("")
	users := map[int64]string{1: "u-one", 2: "u-two"}
	if _, err := store.recordConsensus("session", "local-ranked-singles-1", users, map[int64]int64{1: 1, 2: 1}); err == nil {
		t.Fatal("two winners accepted")
	}
	if got := store.stats("local-ranked-singles-1", "u-one"); got.MatchCount != 0 {
		t.Fatalf("rejected result changed state: %+v", got)
	}
}
