package sync

type DiffOp string

const (
	OpAdd    DiffOp = "add"
	OpUpdate DiffOp = "update"
	OpDelete DiffOp = "delete"
)

type FileDiff struct {
	Path string
	Op   DiffOp
}

// DiffManifests compares two manifests by SHA256 hash. Files in remote but not
// local = add. Files with different hash = update. Files in local but not
// remote are left alone (additive only, no deletes).
func DiffManifests(local, remote *Manifest) []FileDiff {
	localByPath := make(map[string]string, len(local.Files))
	for _, f := range local.Files {
		localByPath[f.Path] = f.SHA256
	}

	var diffs []FileDiff
	for _, rf := range remote.Files {
		localHash, exists := localByPath[rf.Path]
		if !exists {
			diffs = append(diffs, FileDiff{Path: rf.Path, Op: OpAdd})
		} else if localHash != rf.SHA256 {
			diffs = append(diffs, FileDiff{Path: rf.Path, Op: OpUpdate})
		}
	}

	return diffs
}
