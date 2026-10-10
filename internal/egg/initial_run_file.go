package egg

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const initialRunFile = ".egg.run"
const maxInitialRunWire = 8 << 20 // JSON escaping plus metadata for a 1 MiB prompt.

// WriteInitialRunFile transports the admitted request without putting its text
// in the egg wrapper's argv. The child consumes and unlinks this private file.
func WriteInitialRunFile(dir string, request *RunTurnRequest) (string, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	if len(data) > maxInitialRunWire {
		return "", errors.New("initial run request exceeds transport bound")
	}
	path := filepath.Join(dir, initialRunFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Close())
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func ReadInitialRunFile(dir string) (*RunTurnRequest, error) {
	path := filepath.Join(dir, initialRunFile)
	f, err := openBoundRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Mode().Perm() != 0600 {
		return nil, errors.New("initial run file must have permissions 0600")
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxInitialRunWire+1))
	if err := errors.Join(readErr, os.Remove(path)); err != nil {
		return nil, err
	}
	if len(data) > maxInitialRunWire {
		return nil, errors.New("initial run request exceeds transport bound")
	}
	var request RunTurnRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid initial native run request")
	}
	return &request, nil
}
