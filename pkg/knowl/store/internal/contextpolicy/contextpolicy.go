// Package contextpolicy owns backend-independent maintenance context budgets
// and deterministic phase merging.
package contextpolicy

import knowl "github.com/baldaworks/knowl/pkg/knowl/types"

const ControlPageID knowl.PageID = "index"

// CandidateLimit returns the relevance phase target for a bounded page limit.
func CandidateLimit(limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit == 1 {
		return 1
	}
	ordinaryCapacity := limit - 1
	return max(1, (2*ordinaryCapacity+2)/3)
}

// Merge combines already ordered phase results under one exact page bound.
func Merge(limit int, candidates, neighbors, recent []knowl.PageID) []knowl.PageID {
	if limit <= 0 {
		return nil
	}
	seen := make(map[knowl.PageID]struct{}, limit)
	result := make([]knowl.PageID, 0, limit)
	appendIDs := func(ids []knowl.PageID, phaseLimit int) {
		for _, id := range ids {
			if len(result) == limit || phaseLimit == 0 {
				return
			}
			if id == "" {
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			result = append(result, id)
			if phaseLimit > 0 {
				phaseLimit--
			}
		}
	}

	if limit == 1 {
		appendIDs(candidates, 1)
		if len(result) == 0 {
			appendIDs([]knowl.PageID{ControlPageID}, 1)
		}
		if len(result) == 0 {
			appendIDs(recent, 1)
		}
		return result
	}

	candidateLimit := CandidateLimit(limit)
	appendIDs(candidates, candidateLimit)
	ordinaryCapacity := limit - 1
	appendIDs(neighbors, ordinaryCapacity-len(result))
	appendIDs([]knowl.PageID{ControlPageID}, 1)
	appendIDs(candidates[candidateSliceStart(candidates, candidateLimit):], -1)
	appendIDs(recent, -1)
	return result
}

func candidateSliceStart(candidates []knowl.PageID, limit int) int {
	if len(candidates) < limit {
		return len(candidates)
	}
	return limit
}
