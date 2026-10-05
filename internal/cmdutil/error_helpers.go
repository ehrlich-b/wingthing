package cmdutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const maxCLIAPIResponseBytes = 1 << 20

var CLIHTTPClient = &http.Client{Timeout: 15 * time.Second}

func DecodeCLIAPIResponse(body io.Reader, destination any) error {
	data, err := io.ReadAll(io.LimitReader(body, maxCLIAPIResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxCLIAPIResponseBytes {
		return fmt.Errorf("API response exceeds %d bytes", maxCLIAPIResponseBytes)
	}
	return json.Unmarshal(data, destination)
}

type CommandExitError struct {
	Code    int
	Message string
}

func (e *CommandExitError) Error() string { return e.Message }

func ExitError(code int, format string, args ...any) error {
	return &CommandExitError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func Writef(writer io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(writer, format, args...)
	return err
}

func Writeln(writer io.Writer, args ...any) error {
	_, err := fmt.Fprintln(writer, args...)
	return err
}

func CloseWithLog(name string, closer io.Closer) {
	if err := closer.Close(); err != nil {
		log.Printf("close %s: %v", name, err)
	}
}

func CloseAndJoin(name string, closer io.Closer, prior error) error {
	if err := closer.Close(); err != nil {
		return errors.Join(prior, fmt.Errorf("close %s: %w", name, err))
	}
	return prior
}

func RemoveWithLog(path string) {
	if err := RemoveIfExists(path); err != nil {
		log.Printf("remove %s: %v", path, err)
	}
}

func RemoveIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func RemoveFiles(paths ...string) error {
	var result error
	for _, path := range paths {
		if err := RemoveIfExists(path); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}
