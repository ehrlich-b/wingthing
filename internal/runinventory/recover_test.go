package runinventory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestRunInventoryConsumerRejectsIncompleteDuplicateAndForeignPages(t *testing.T) {
	base := map[string]any{"wing_id": "wing", "snapshot_revision": "snapshot", "count": 1, "runs": []any{map[string]any{"wing_id": "wing", "run_id": "run", "cleanup": "unknown"}}}
	for _, scenario := range []string{"missing", "bounded", "repeat-cursor", "duplicate", "foreign", "revision", "unknown-peer"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			call := func(_ context.Context, name string, _ json.RawMessage) (map[string]any, error) {
				if name != "agent_list" {
					t.Fatalf("read before full scan: %s", name)
				}
				calls++
				data := map[string]any{}
				for k, v := range base {
					data[k] = v
				}
				data["next_cursor"] = "cursor"
				switch scenario {
				case "missing":
					delete(data, "runs")
				case "foreign":
					data["runs"] = []any{map[string]any{"wing_id": "other", "run_id": "run"}}
				case "revision":
					if calls > 1 {
						data["snapshot_revision"] = "changed"
					}
				case "duplicate":
					if calls > 1 {
						data["next_cursor"] = ""
					}
				case "repeat-cursor":
					if calls > 1 {
						data["runs"] = []any{map[string]any{"wing_id": "wing", "run_id": "run-2"}}
					}
				case "unknown-peer":
					return nil, errors.ErrUnsupported
				}
				return data, nil
			}
			pages := 3
			if scenario == "bounded" {
				pages = 1
			}
			out, err := Recover(t.Context(), call, Options{MaxPages: pages})
			if err == nil || len(out.Runs) != 0 || out.Complete {
				t.Fatalf("incomplete appeared complete: %+v %v", out, err)
			}
		})
	}
}

func TestRunInventoryConsumerEmptyAndReadErrorsAreDistinct(t *testing.T) {
	empty := func(context.Context, string, json.RawMessage) (map[string]any, error) {
		return map[string]any{"wing_id": "wing", "snapshot_revision": "snapshot", "count": 0, "runs": []any{}}, nil
	}
	out, err := Recover(t.Context(), empty, Options{})
	if err != nil || !out.Complete || out.Counts.Total != 0 {
		t.Fatalf("valid empty: %+v %v", out, err)
	}
	calls := []string{}
	call := func(_ context.Context, name string, _ json.RawMessage) (map[string]any, error) {
		calls = append(calls, name)
		if name == "agent_list" {
			return map[string]any{"wing_id": "wing", "snapshot_revision": "snapshot", "count": 1, "runs": []any{map[string]any{"wing_id": "wing", "run_id": "lost", "cleanup": "unknown"}}}, nil
		}
		return nil, errors.New("run unavailable")
	}
	out, err = Recover(t.Context(), call, Options{})
	if err != nil || out.Counts.UnavailableRun != 1 || out.Runs[0].State != "unavailable" || !reflect.DeepEqual(calls, []string{"agent_list", "agent_status"}) {
		t.Fatalf("missing != done: %+v %v %v", out, err, calls)
	}
}

func TestRunInventoryConsumerUsesTerminalReceiptLifecycle(t *testing.T) {
	for _, test := range []struct {
		name, status, failure, wantState string
		wantReadError                    bool
	}{
		{"cancelled", "stopped", "stopped", "interrupted", false},
		{"stopped-kind", "done", "stopped", "interrupted", false},
		{"unknown-outcome", "failed", "unknown_outcome", "interrupted", false},
		{"provider-exit", "failed", "provider_exit", "interrupted", false},
		{"other-failure", "failed", "auth_failed", "terminal_other", false},
		{"failed", "failed", "", "terminal_other", false},
		{"missing-lifecycle", "", "", "unavailable_output", true},
		{"nonterminal", "running", "", "unavailable_output", true},
		{"completed", "done", "", "completed", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := func(_ context.Context, name string, _ json.RawMessage) (map[string]any, error) {
				switch name {
				case "agent_list":
					return map[string]any{"wing_id": "wing", "snapshot_revision": "snapshot", "count": 1, "runs": []any{map[string]any{"wing_id": "wing", "run_id": "run", "cleanup": "unknown", "output_availability": "available"}}}, nil
				case "agent_status":
					return map[string]any{"run_id": "run", "status": "done"}, nil
				case "agent_result":
					return map[string]any{"run_id": "run", "ready": true, "status": test.status, "failure_kind": test.failure, "output": "receipt output"}, nil
				default:
					t.Fatalf("unexpected operation: %s", name)
					return nil, nil
				}
			}
			out, err := Recover(t.Context(), call, Options{})
			if err != nil || !out.Complete || out.Counts.Total != 1 || len(out.Runs) != 1 {
				t.Fatalf("recovery: %+v %v", out, err)
			}
			r := out.Runs[0]
			if r.State != test.wantState || (r.ReadError != "") != test.wantReadError {
				t.Fatalf("receipt lifecycle was ignored: %+v", out)
			}
			if test.wantState == "completed" {
				if out.Counts.Completed != 1 || r.Output != "receipt output" {
					t.Fatalf("successful receipt was lost: %+v", out)
				}
			} else if out.Counts.Completed != 0 || r.Output != "" {
				t.Fatalf("unsuccessful receipt reported completion: %+v", out)
			}
			if !test.wantReadError && r.Status != test.status {
				t.Fatalf("result status was lost: %+v", r)
			}
		})
	}
}
