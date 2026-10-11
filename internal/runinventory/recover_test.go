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
