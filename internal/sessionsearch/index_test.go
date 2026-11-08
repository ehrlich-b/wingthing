package sessionsearch

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func decodeText(data []byte) (string, bool) {
	var record struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(data, &record) != nil {
		return "", false
	}
	return record.Text, true
}
func record(text string) []byte {
	data, _ := json.Marshal(map[string]string{"text": text})
	return append(data, '\n')
}
func indexFixture(t *testing.T) (*Index, string, string) {
	t.Helper()
	state := testsocket.Dir(t)
	dir := filepath.Join(state, "session")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	index, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	return index, state, dir
}
func assertMatches(t *testing.T, index *Index, words []string, want []bool) {
	t.Helper()
	got, err := index.Matches(t.Context(), "id", words)
	if err != nil {
		t.Fatal(err)
	}
	for n := range want {
		if got[n] != want[n] {
			t.Fatalf("word %q hit=%t want=%t", words[n], got[n], want[n])
		}
	}
}
func refresh(t *testing.T, index *Index, dir, name string) (int64, bool) {
	t.Helper()
	budget := int64(ReadBudget)
	full, err := index.Refresh(t.Context(), "id", dir, name, &budget, decodeText)
	if err != nil {
		t.Fatal(err)
	}
	return ReadBudget - budget, full
}

func TestSessionSearchIndexAppendReopenReplaceTruncateDelete(t *testing.T) {
	index, state, dir := indexFixture(t)
	path := filepath.Join(dir, "lifecycle.jsonl")
	first := record("DNS helper café 網絡")
	if err := os.WriteFile(path, first, 0600); err != nil {
		t.Fatal(err)
	}
	if read, full := refresh(t, index, dir, "lifecycle.jsonl"); !full || read > int64(len(first))+2048 {
		t.Fatalf("initial read=%d full=%t", read, full)
	}
	assertMatches(t, index, []string{"dns", "helper", "fé", "網絡", "missing", "%", "\""}, []bool{true, true, true, true, false, false, false})
	if read, full := refresh(t, index, dir, "lifecycle.jsonl"); read != 0 || !full {
		t.Fatalf("warm index reread=%d full=%t", read, full)
	}
	_ = index.Close()
	var err error
	index, err = Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	second := record("incremental append")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(second)
	_ = f.Close()
	if read, full := refresh(t, index, dir, "lifecycle.jsonl"); read > int64(len(second))+4096 || !full {
		t.Fatalf("append read=%d expected=%d full=%t", read, len(second), full)
	}
	assertMatches(t, index, []string{"dns", "append"}, []bool{true, true})
	_ = os.WriteFile(path, record("new"), 0600)
	refresh(t, index, dir, "lifecycle.jsonl")
	assertMatches(t, index, []string{"dns", "append", "new"}, []bool{false, false, true})
	_ = os.WriteFile(path+".replacement", record("replacement message"), 0600)
	_ = os.Rename(path+".replacement", path)
	refresh(t, index, dir, "lifecycle.jsonl")
	assertMatches(t, index, []string{"new", "replacement"}, []bool{false, true})
	_ = os.Remove(path)
	refresh(t, index, dir, "lifecycle.jsonl")
	assertMatches(t, index, []string{"replacement"}, []bool{false})
}

func TestSessionSearchIndexPartialMalformedBoundsAndFileSafety(t *testing.T) {
	index, _, dir := indexFixture(t)
	path := filepath.Join(dir, "lifecycle.jsonl")
	data := record("event arrives in parts")
	_ = os.WriteFile(path, data[:len(data)-1], 0600)
	if _, full := refresh(t, index, dir, "lifecycle.jsonl"); full {
		t.Fatal("partial record claimed complete")
	}
	assertMatches(t, index, []string{"arrives"}, []bool{false})
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.Write([]byte{'\n'})
	_ = f.Close()
	if _, full := refresh(t, index, dir, "lifecycle.jsonl"); !full {
		t.Fatal("completed record stayed partial")
	}
	assertMatches(t, index, []string{"arrives"}, []bool{true})
	_ = os.WriteFile(path, append([]byte("invalid json\n"), record("valid")...), 0600)
	if _, full := refresh(t, index, dir, "lifecycle.jsonl"); full {
		t.Fatal("malformed source claimed complete")
	}
	assertMatches(t, index, []string{"valid"}, []bool{true})
	// Ordinary finite document exercises the retained-text bound.
	_ = os.WriteFile(path, record(strings.Repeat("ordinary message ", TextLimit/17+1)), 0600)
	if _, full := refresh(t, index, dir, "lifecycle.jsonl"); full {
		t.Fatal("text cap hidden")
	}
	_ = os.WriteFile(path, record("bounded read"), 0600)
	budget := int64(8)
	full, err := index.Refresh(t.Context(), "id", dir, "lifecycle.jsonl", &budget, decodeText)
	if err != nil || full || budget < 0 || budget > 8 {
		t.Fatalf("read cap %t %d %v", full, budget, err)
	}
	refresh(t, index, dir, "lifecycle.jsonl")
	assertMatches(t, index, []string{"bounded"}, []bool{true})
	_ = os.Remove(path)
	_ = os.Symlink("elsewhere", path)
	if _, err := index.Refresh(t.Context(), "id", dir, "lifecycle.jsonl", &budget, decodeText); err == nil {
		t.Fatal("symlink accepted")
	}
	_ = os.Remove(path)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Refresh(t.Context(), "id", dir, "lifecycle.jsonl", &budget, decodeText); err == nil {
		t.Fatal("directory accepted")
	}
	var pages int
	if err := index.db.QueryRow("PRAGMA max_page_count").Scan(&pages); err != nil || pages != 16384 {
		t.Fatalf("database cap %d %v", pages, err)
	}
}

func TestSessionSearchIndexChangedContentCannotMatchAfterZeroBudgetOrRegrowth(t *testing.T) {
	index, _, dir := indexFixture(t)
	path := filepath.Join(dir, "lifecycle.jsonl")
	_ = os.WriteFile(path, record("oldword"), 0600)
	refresh(t, index, dir, "lifecycle.jsonl")
	assertMatches(t, index, []string{"oldword"}, []bool{true})
	// os.WriteFile keeps the inode while truncating and writing a larger file.
	_ = os.WriteFile(path, append(record("newword"), record("second message")...), 0600)
	if _, full := refresh(t, index, dir, "lifecycle.jsonl"); !full {
		t.Fatal("regrowth did not rebuild")
	}
	assertMatches(t, index, []string{"oldword", "newword", "second"}, []bool{false, true, true})
	_ = os.WriteFile(path+".replacement", record("replacement"), 0600)
	_ = os.Rename(path+".replacement", path)
	budget := int64(0)
	full, err := index.Refresh(t.Context(), "id", dir, "lifecycle.jsonl", &budget, decodeText)
	if err != nil || full {
		t.Fatal("zero-budget replacement claimed complete", err)
	}
	assertMatches(t, index, []string{"newword", "second", "replacement"}, []bool{false, false, false})
	refresh(t, index, dir, "lifecycle.jsonl")
	assertMatches(t, index, []string{"replacement"}, []bool{true})
	// Even a legitimate append needs its changed prefix established; an empty
	// budget must not let an unverified older snapshot participate in matching.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.Write(record("append"))
	_ = f.Close()
	full, err = index.Refresh(t.Context(), "id", dir, "lifecycle.jsonl", &budget, decodeText)
	if err != nil || full {
		t.Fatal("zero-budget growth claimed complete", err)
	}
	assertMatches(t, index, []string{"replacement"}, []bool{false})
}

func TestSessionSearchIndexGzipAndRecognizedSecretsNeverPersistOrMatch(t *testing.T) {
	index, state, dir := indexFixture(t)
	f, err := os.Create(filepath.Join(dir, "chat.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	// All values below are fabricated fixture credentials.
	secrets := []string{"sk-fixtureSECRET123456789", "ghp_fixtureSECRET123456789", "fixturepassword", "fixturebearer", "eyJmaXh0dXJl.eyJwYXlsb2Fk.c2lnbmF0dXJl"}
	text := "public DNS helper\nAPI_KEY=" + secrets[0] + "\npassword: " + secrets[2] + "\nAuthorization: Bearer " + secrets[3] + "\n" + secrets[1] + "\n" + secrets[4] + "\n-----BEGIN PRIVATE KEY-----\nfixturekeymaterial\n-----END PRIVATE KEY-----\n"
	_, _ = gz.Write(record(text))
	_ = gz.Close()
	_ = f.Close()
	if _, full := refresh(t, index, dir, "chat.jsonl.gz"); !full {
		t.Fatal("archive incomplete")
	}
	if read, full := refresh(t, index, dir, "chat.jsonl.gz"); read != 0 || !full {
		t.Fatal("unchanged archive reread")
	}
	words := append([]string{"dns", "fixturekeymaterial"}, secrets...)
	want := make([]bool, len(words))
	want[0] = true
	assertMatches(t, index, words, want)
	_ = index.Close()
	data, err := os.ReadFile(filepath.Join(state, "session-search-v2", "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range append(secrets, "fixturekeymaterial") {
		if strings.Contains(string(data), secret) {
			t.Fatal("recognized secret persisted")
		}
	}
	info, err := os.Stat(filepath.Join(state, "session-search-v2", "index.sqlite"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("cache permissions")
	}
}

func TestSearchExcerptIsBoundedAndSessionQualified(t *testing.T) {
	index, _, dir := indexFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "lifecycle.jsonl"), record(strings.Repeat("context ", 40)+"dns helper fixed"+strings.Repeat(" context", 40)), 0600); err != nil {
		t.Fatal(err)
	}
	refresh(t, index, dir, "lifecycle.jsonl")
	text, err := index.Excerpt(t.Context(), "id", []string{"dns", "helper"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "dns helper fixed") || len([]rune(text)) > 240 {
		t.Fatalf("excerpt: %q", text)
	}
	other, err := index.Excerpt(t.Context(), "not-admitted", []string{"dns"})
	if err != nil || other != "" {
		t.Fatalf("cross-session excerpt: %q %v", other, err)
	}
}
