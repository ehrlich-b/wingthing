package main

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

var cliHTTPClient = &http.Client{Timeout: 15 * time.Second}

func decodeCLIAPIResponse(body io.Reader, destination any) error {
	data, err := io.ReadAll(io.LimitReader(body, maxCLIAPIResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxCLIAPIResponseBytes {
		return fmt.Errorf("API response exceeds %d bytes", maxCLIAPIResponseBytes)
	}
	return json.Unmarshal(data, destination)
}

type commandExitError struct {
	code    int
	message string
}

func (e *commandExitError) Error() string { return e.message }

func exitError(code int, format string, args ...any) error {
	return &commandExitError{code: code, message: fmt.Sprintf(format, args...)}
}

func writef(writer io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(writer, format, args...)
	return err
}

func writeln(writer io.Writer, args ...any) error {
	_, err := fmt.Fprintln(writer, args...)
	return err
}

func closeWithLog(name string, closer io.Closer) {
	if err := closer.Close(); err != nil {
		log.Printf("close %s: %v", name, err)
	}
}

func closeAndJoin(name string, closer io.Closer, prior error) error {
	if err := closer.Close(); err != nil {
		return errors.Join(prior, fmt.Errorf("close %s: %w", name, err))
	}
	return prior
}

func removeWithLog(path string) {
	if err := removeIfExists(path); err != nil {
		log.Printf("remove %s: %v", path, err)
	}
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeFiles(paths ...string) error {
	var result error
	for _, path := range paths {
		if err := removeIfExists(path); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}
