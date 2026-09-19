package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const (
	rankedInitialRating = 1500
	rankedRatingStep    = 30
	rankedPointStep     = 1
)

type rankedStats struct {
	MatchCount      int32 `json:"match_count"`
	WinCount        int32 `json:"win_count"`
	LoseCount       int32 `json:"lose_count"`
	DrawCount       int32 `json:"draw_count"`
	DisconnectCount int32 `json:"disconnect_count"`
	NoContestCount  int32 `json:"no_contest_count"`
	WinStreakCount  int32 `json:"win_streak_count"`
	LoseStreakCount int32 `json:"lose_streak_count"`
	Rating          int32 `json:"rating"`
	Rank            int32 `json:"rank"`
	Point           int32 `json:"point"`
	MaxPoint        int32 `json:"max_point"`
}

type rankedPersistentState struct {
	Participants map[string]rankedStats `json:"participants"`
	Processed    map[string]bool        `json:"processed_sessions"`
}

type rankedStore struct {
	mu           sync.Mutex
	path         string
	participants map[string]rankedStats
	processed    map[string]bool
}

func defaultRankedStats() rankedStats {
	return rankedStats{Rating: rankedInitialRating, Rank: 1}
}

func rankedParticipantKey(competitionID, uid string) string {
	return competitionID + "\x00" + uid
}

func newRankedStore(path string) *rankedStore {
	s := &rankedStore{path: path, participants: make(map[string]rankedStats), processed: make(map[string]bool)}
	if path == "" {
		return s
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s
	}
	if err != nil {
		log.Printf("[NPLN Timber] ranked state read failed path=%q error=%v", path, err)
		return s
	}
	var state rankedPersistentState
	if err := json.Unmarshal(b, &state); err != nil {
		log.Printf("[NPLN Timber] ranked state decode failed path=%q error=%v", path, err)
		return s
	}
	if state.Participants != nil {
		s.participants = state.Participants
	}
	if state.Processed != nil {
		s.processed = state.Processed
	}
	return s
}

func (s *rankedStore) stats(competitionID, uid string) rankedStats {
	if s == nil {
		return defaultRankedStats()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stats, ok := s.participants[rankedParticipantKey(competitionID, uid)]
	if !ok {
		return defaultRankedStats()
	}
	return stats
}

func (s *rankedStore) battleStats(uid string) (battles, disconnects int32) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, stats := range s.participants {
		if len(key) > len(uid) && key[len(key)-len(uid)-1:] == "\x00"+uid {
			battles += stats.MatchCount
			disconnects += stats.DisconnectCount
		}
	}
	return battles, disconnects
}

func applyRankedResult(stats rankedStats, result int64) rankedStats {
	if stats.Rating == 0 {
		stats = defaultRankedStats()
	}
	stats.MatchCount++
	switch result {
	case 0: // BATTLE_LOSE
		stats.LoseCount++
		stats.LoseStreakCount++
		stats.WinStreakCount = 0
		stats.Rating -= rankedRatingStep
		if stats.Rating < 0 {
			stats.Rating = 0
		}
		stats.Point -= rankedPointStep
		if stats.Point < 0 {
			stats.Point = 0
		}
	case 1: // BATTLE_WIN
		stats.WinCount++
		stats.WinStreakCount++
		stats.LoseStreakCount = 0
		stats.Rating += rankedRatingStep
		stats.Point += rankedPointStep
		if stats.Point > stats.MaxPoint {
			stats.MaxPoint = stats.Point
		}
	case 2: // BATTLE_DRAW
		stats.DrawCount++
		stats.WinStreakCount = 0
		stats.LoseStreakCount = 0
	case 3: // BATTLE_NO_CONTEST
		stats.NoContestCount++
		stats.WinStreakCount = 0
		stats.LoseStreakCount = 0
	}
	return stats
}

// recordConsensus applies a battle exactly once, and only after both clients
// have independently published the same participant-result map.
func (s *rankedStore) recordConsensus(gameSession, competitionID string, sequenceUID map[int64]string, results map[int64]int64) (bool, error) {
	if s == nil {
		return false, nil
	}
	if !validRankedCompetitionIDString(competitionID) || len(sequenceUID) != 2 || len(results) != 2 {
		return false, fmt.Errorf("invalid ranked result envelope")
	}
	for _, sequence := range []int64{1, 2} {
		if sequenceUID[sequence] == "" || results[sequence] < 0 || results[sequence] > 3 {
			return false, fmt.Errorf("invalid ranked participant result")
		}
	}
	validPair := (results[1] == 1 && results[2] == 0) || (results[1] == 0 && results[2] == 1) ||
		(results[1] == 2 && results[2] == 2) || (results[1] == 3 && results[2] == 3)
	if !validPair {
		return false, fmt.Errorf("inconsistent ranked participant results")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	key := competitionID + "\x00" + gameSession
	if s.processed[key] {
		return false, nil
	}
	for _, sequence := range []int64{1, 2} {
		participantKey := rankedParticipantKey(competitionID, sequenceUID[sequence])
		stats, ok := s.participants[participantKey]
		if !ok {
			stats = defaultRankedStats()
		}
		s.participants[participantKey] = applyRankedResult(stats, results[sequence])
	}
	s.processed[key] = true
	if err := s.persistLocked(); err != nil {
		log.Printf("[NPLN Timber] ranked state persistence failed path=%q error=%v", s.path, err)
	}
	return true, nil
}

func (s *rankedStore) persistLocked() error {
	if s.path == "" {
		return nil
	}
	state := rankedPersistentState{Participants: s.participants, Processed: s.processed}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, b, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, s.path)
}
