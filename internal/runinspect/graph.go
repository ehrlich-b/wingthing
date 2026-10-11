package runinspect

import "sort"

func (report *Report) identityGraph(r *records) {
	conversations := map[string][]conversationRow{}
	executions := map[string][]executionRow{}
	wingSessions := map[string][]wingRow{}
	for _, c := range r.conversations {
		conversations[c.id] = append(conversations[c.id], c)
	}
	for _, e := range r.executions {
		executions[e.session] = append(executions[e.session], e)
	}
	for _, w := range r.wings {
		wingSessions[w.session] = append(wingSessions[w.session], w)
	}
	for i := range report.Runs {
		run := &report.Runs[i]
		claims := wingSessions[run.SessionID]
		if len(claims) > 1 {
			ids := []string{run.SessionID}
			refs := []Ref{}
			for _, claim := range claims {
				ids = append(ids, claim.id)
				refs = append(refs, claim.source)
			}
			run.issue("execution_claimed_by_multiple_runs", ids, refs...)
			report.edge("execution_owner", run.ID, run.SessionID, "inconsistent", refs...)
		}
		links := executions[run.SessionID]
		if len(links) > 1 {
			ids := []string{run.SessionID}
			refs := []Ref{}
			for _, link := range links {
				ids = append(ids, link.conversation)
				refs = append(refs, link.source)
			}
			run.issue("execution_claimed_by_multiple_conversations", ids, refs...)
			continue
		}
		if len(links) == 1 {
			link := links[0]
			run.ConversationID = link.conversation
			cs := conversations[link.conversation]
			state := "observed"
			if len(cs) != 1 {
				state = "absent"
				if len(cs) > 1 {
					state = "inconsistent"
				}
				run.issue("execution_conversation_unavailable", []string{run.SessionID, link.conversation}, link.source)
			} else {
				run.CurrentSessionID = cs[0].session
				if claims := wingSessions[run.SessionID]; len(claims) == 1 && claims[0].valid {
					owner := claims[0].record.Launch.Authority.UserID
					if owner != "" && owner != cs[0].owner {
						state = "inconsistent"
						run.issue("run_conversation_owner_conflict", []string{run.ID, run.SessionID, cs[0].id}, claims[0].source, cs[0].source)
					}
				}
				if cs[0].session != run.SessionID {
					report.edge("replacement_execution", run.SessionID, cs[0].session, "observed", link.source, cs[0].source)
				}
			}
			report.edge("execution_conversation", run.SessionID, link.conversation, state, link.source)
		}
	}
	byRun := map[string]int{}
	for i, run := range report.Runs {
		byRun[run.ID] = i
	}
	for i := range report.Runs {
		run := &report.Runs[i]
		if p, ok := byRun[run.ParentID]; ok {
			parent := report.Runs[p]
			parentClaims, childClaims := wingSessions[parent.SessionID], wingSessions[run.SessionID]
			if len(parentClaims) == 1 && len(childClaims) == 1 && parentClaims[0].valid && childClaims[0].valid {
				a, b := parentClaims[0].record.Launch.Authority, childClaims[0].record.Launch.Authority
				if a.Principal != b.Principal || a.UserID != b.UserID {
					run.issue("parent_owner_conflict", []string{run.ID, run.ParentID}, parentClaims[0].source, childClaims[0].source)
				}
			}
		}
		seen := map[string]bool{}
		cursor := run.ID
		chain := []string{}
		for cursor != "" {
			if seen[cursor] {
				run.issue("run_parent_cycle", chain, run.Sources...)
				break
			}
			seen[cursor] = true
			chain = append(chain, cursor)
			p, ok := byRun[cursor]
			if !ok {
				break
			}
			cursor = report.Runs[p].ParentID
		}
	}
	order := make([]string, 0, len(conversations))
	for id := range conversations {
		order = append(order, id)
	}
	sort.Strings(order)
	for _, id := range order {
		cs := conversations[id]
		if len(cs) != 1 {
			refs := []Ref{}
			for _, c := range cs {
				refs = append(refs, c.source)
			}
			report.Issues = append(report.Issues, Issue{Code: "conflicting_conversation_records", IDs: []string{id}, Sources: refs})
			continue
		}
		c := cs[0]
		view := Conversation{ID: c.id, ParentID: c.parent, RootID: c.root, SessionID: c.session, Executions: []string{}, Sources: []Ref{c.source}, Issues: []Issue{}, Checkpoint: Checkpoint{State: "unknown"}}
		issue := func(code string, ids []string, refs ...Ref) {
			view.Issues = append(view.Issues, Issue{Code: code, IDs: ids, Sources: refs})
		}
		for _, e := range r.executions {
			if e.conversation == id {
				view.Executions = append(view.Executions, e.session)
			}
		}
		sort.Strings(view.Executions)
		found := false
		for _, session := range view.Executions {
			if session == view.SessionID {
				found = true
			}
		}
		if !found {
			issue("current_execution_absent", []string{id, c.session}, c.source)
		}
		for _, relation := range []struct{ kind, target string }{{"conversation_root", c.root}, {"conversation_parent", c.parent}} {
			if relation.target == "" {
				if relation.kind == "conversation_root" {
					issue("missing_root_identity", []string{id}, c.source)
				}
				continue
			}
			state := "observed"
			targets := conversations[relation.target]
			if len(targets) == 0 {
				state = "absent"
				issue("missing_conversation_ancestor", []string{id, relation.target}, c.source)
			} else if len(targets) > 1 {
				state = "inconsistent"
				issue("conflicting_conversation_ancestor", []string{id, relation.target}, c.source)
			} else if targets[0].owner != c.owner || targets[0].root != c.root {
				state = "inconsistent"
				issue("conversation_ancestor_owner_or_root_conflict", []string{id, relation.target}, c.source, targets[0].source)
			}
			report.edge(relation.kind, id, relation.target, state, c.source)
		}
		seen := map[string]bool{}
		cursor := id
		chain := []string{}
		for cursor != "" {
			if seen[cursor] {
				issue("conversation_parent_cycle", chain, c.source)
				break
			}
			seen[cursor] = true
			chain = append(chain, cursor)
			parents := conversations[cursor]
			if len(parents) != 1 {
				break
			}
			cursor = parents[0].parent
		}
		report.Conversations = append(report.Conversations, view)
	}
	for _, e := range r.executions {
		if len(conversations[e.conversation]) == 0 {
			report.Issues = append(report.Issues, Issue{Code: "execution_missing_conversation", IDs: []string{e.session, e.conversation}, Sources: []Ref{e.source}})
		}
	}
}

func (report *Report) finalizeEvidence() {
	for i := range report.Runs {
		run := &report.Runs[i]
		run.EvidenceState = "consistent"
		for _, issue := range run.Issues {
			switch issue.Code {
			case "missing_parent", "partial_run_record", "partial_or_unsupported_egg_record", "execution_conversation_unavailable":
				if run.EvidenceState == "consistent" {
					run.EvidenceState = "incomplete"
				}
			default:
				run.EvidenceState = "inconsistent"
			}
		}
		if run.EvidenceState == "inconsistent" {
			run.Diagnosis = "inconsistent_evidence"
		}
		run.Actions = []Action{{Kind: "inspect", Basis: run.Sources, Requires: []string{"Review the cited records and snapshot capture time."}}}
		if run.Diagnosis == "running_at_snapshot" && run.EvidenceState == "consistent" {
			run.Actions = append(run.Actions, Action{Kind: "wait_for_known_completion", Basis: run.Sources, Requires: []string{"Verify the same execution is still alive on the host; a snapshot cannot establish current liveness."}})
		}
		if run.EvidenceState != "consistent" || run.Diagnosis == "missing_evidence" || run.Diagnosis == "admitted_outcome_unknown" || run.Diagnosis == "completed_output_unavailable" {
			run.Actions = append(run.Actions, Action{Kind: "missing_evidence", Basis: run.Sources, Requires: []string{"Resolve the cited graph, receipt or output gaps before any manual recovery."}})
		}

	}
}
