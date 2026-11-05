package sub

// The full remark template goes to each identity's first served link; if probe ordering moves another
// entry to the front, the subscription is rendered again with the full template on that entry.

// remarkSpan is the range of templated remarks (indices into SubService.remarkCalls) one
// rendered entry carries.
type remarkSpan struct{ from, to int }

func (s *SubService) remarkMark() int { return len(s.remarkCalls) }

func (s *SubService) remarkSince(from int) remarkSpan {
	return remarkSpan{from, len(s.remarkCalls)}
}

// splitRemarkSpan maps the n entries one render produced onto its remarks: one remark each when
// the counts match, otherwise every entry carries the whole span.
func splitRemarkSpan(span remarkSpan, n int) []remarkSpan {
	out := make([]remarkSpan, n)
	for i := range out {
		if span.to-span.from == n {
			out[i] = remarkSpan{span.from + i, span.from + i + 1}
		} else {
			out[i] = span
		}
	}
	return out
}

// remarkHolders names, per identity, the first templated remark among the served entries, which
// are listed in the order the subscription serves them.
func (s *SubService) remarkHolders(served []remarkSpan) map[string]int {
	holders := map[string]int{}
	for _, span := range served {
		for call := span.from; call < span.to && call < len(s.remarkCalls); call++ {
			if _, ok := holders[s.remarkCalls[call]]; !ok {
				holders[s.remarkCalls[call]] = call
			}
		}
	}
	return holders
}

// rerenderForRemarks reports whether the full template went to a remark other than the one now
// served first, and if so prepares the request for the second pass that fixes it.
func (s *SubService) rerenderForRemarks(served []remarkSpan) bool {
	if s.remarkFullAt != nil || s.subInfoNodeEnable || s.remarkTemplate == "" || !s.subscriptionBody || s.probeVerdicts() == nil {
		return false
	}
	holders := s.remarkHolders(served)
	first := map[string]int{}
	for call, key := range s.remarkCalls {
		if _, ok := first[key]; !ok {
			first[key] = call
		}
	}
	moved := false
	for key, call := range holders {
		if first[key] != call {
			moved = true
			break
		}
	}
	if !moved {
		return false
	}
	s.remarkFullAt = holders
	s.remarkCalls = nil
	s.usageShown = map[string]bool{}
	s.rendered = 0
	return true
}
