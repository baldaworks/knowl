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
	ids, _ := MergeWithReasons(limit, candidates, neighbors, recent, nil)
	return ids
}

// ChannelReasons records actual channel membership before phase merging.
func ChannelReasons(lexical, vector []knowl.PageID) map[knowl.PageID]knowl.ContextSelectionReason {
	reasons := make(map[knowl.PageID]knowl.ContextSelectionReason, len(lexical)+len(vector))
	for _, id := range lexical {
		reasons[id] = knowl.ContextLexical
	}
	for _, id := range vector {
		if reasons[id] == knowl.ContextLexical {
			reasons[id] = knowl.ContextHybrid
		} else {
			reasons[id] = knowl.ContextVector
		}
	}
	return reasons
}

// MergeWithReasons preserves Merge ordering and records the first winning phase.
func MergeWithReasons(limit int, candidates, neighbors, recent []knowl.PageID, channels map[knowl.PageID]knowl.ContextSelectionReason) ([]knowl.PageID, map[knowl.PageID]knowl.ContextSelectionReason) {
	if limit <= 0 {
		return nil, nil
	}
	reasons := make(map[knowl.PageID]knowl.ContextSelectionReason, limit)
	seen := make(map[knowl.PageID]struct{}, limit)
	result := make([]knowl.PageID, 0, limit)
	appendIDs := func(ids []knowl.PageID, phaseLimit int, phase knowl.ContextSelectionReason) {
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
			if id != ControlPageID {
				reason := phase
				if reason == knowl.ContextUnknown && channels[id] != "" {
					reason = channels[id]
				}
				reasons[id] = reason
			}
			if phaseLimit > 0 {
				phaseLimit--
			}
		}
	}

	if limit == 1 {
		appendIDs(candidates, 1, knowl.ContextUnknown)
		if len(result) == 0 {
			appendIDs([]knowl.PageID{ControlPageID}, 1, knowl.ContextUnknown)
		}
		if len(result) == 0 {
			appendIDs(recent, 1, knowl.ContextRecent)
		}
		return result, reasons
	}

	candidateLimit := CandidateLimit(limit)
	appendIDs(candidates, candidateLimit, knowl.ContextUnknown)
	ordinaryCapacity := limit - 1
	appendIDs(neighbors, ordinaryCapacity-len(result), knowl.ContextNeighbor)
	appendIDs([]knowl.PageID{ControlPageID}, 1, knowl.ContextUnknown)
	appendIDs(candidates[candidateSliceStart(candidates, candidateLimit):], -1, knowl.ContextUnknown)
	appendIDs(recent, -1, knowl.ContextRecent)
	return result, reasons
}

func candidateSliceStart(candidates []knowl.PageID, limit int) int {
	if len(candidates) < limit {
		return len(candidates)
	}
	return limit
}
