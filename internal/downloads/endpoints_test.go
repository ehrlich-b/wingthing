package downloads

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDownloadEndpointsNeverUseGitHub(t *testing.T) {
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = "../.."
	files, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	forbidden := regexp.MustCompile(`api[.]github[.]com/repos/|github[.]com/(ehrlich-b/[[:alnum:]_.-]+|\$\{REPO\})/releases`)
	for _, name := range strings.Split(string(files), "\x00") {
		if strings.HasSuffix(name, "_test.go") || strings.Contains(name, "/testdata/") {
			continue
		}
		switch filepath.Ext(name) {
		case ".go", ".sh", ".tmpl", ".js", ".ts", ".tsx", ".html":
		default:
			continue
		}
		body, err := os.ReadFile(filepath.Join("../..", name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if forbidden.Match(body) {
			t.Errorf("%s contains a retired product download endpoint", name)
		}
	}
}
