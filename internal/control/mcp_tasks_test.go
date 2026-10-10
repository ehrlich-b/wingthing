package control

import (
	"fmt"
	"testing"
)

func TestMCPTaskQualificationAndPagination(t *testing.T) {
	for _, wing := range []string{"wing:one", "wing/two", "Ω"} {
		id := QualifyTaskID(wing, "run:three")
		gotWing, gotRun, err := SplitTaskID(id)
		if err != nil || gotWing != wing || gotRun != "run:three" {
			t.Fatalf("%s: %s %s %v", id, gotWing, gotRun, err)
		}
	}
	for _, id := range []string{"run", "wt::", "wt:***:YQ", "other:YQ:Yg"} {
		if _, _, err := SplitTaskID(id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	tasks := []MCPTask{}
	for i := 0; i < MCPTaskPageSize+1; i++ {
		tasks = append(tasks, MCPTask{TaskID: fmt.Sprintf("run-%03d", i)})
	}
	page, err := PageTasks(tasks, "")
	if err != nil || len(page["tasks"].([]MCPTask)) != MCPTaskPageSize {
		t.Fatalf("page: %v %v", page, err)
	}
	// Removal between pages must not skip the last task.
	last, err := PageTasks(tasks[1:], page["nextCursor"].(string))
	if err != nil || len(last["tasks"].([]MCPTask)) != 1 || last["nextCursor"] != nil {
		t.Fatalf("last page: %v %v", last, err)
	}
	if _, err := PageTasks(tasks, "invalid"); err == nil {
		t.Fatal("accepted invalid cursor")
	}
}
