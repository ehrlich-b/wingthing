package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

type Client struct {
	socketPath string
	http       *http.Client
}

func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		},
	}
}

type SubmitTaskRequest struct {
	What  string `json:"what"`
	Type  string `json:"type,omitempty"`
	Agent string `json:"agent,omitempty"`
	RunAt string `json:"run_at,omitempty"`
}

func (c *Client) SubmitTask(req SubmitTaskRequest) (*taskResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.post("/tasks", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusCreated); err != nil {
		return nil, err
	}
	var t taskResponse
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &t, nil
}

func (c *Client) ListTasks(status string, limit int) ([]taskResponse, error) {
	path := "/tasks?"
	if status != "" {
		path += "status=" + status + "&"
	}
	if limit > 0 {
		path += fmt.Sprintf("limit=%d&", limit)
	}
	resp, err := c.get(path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var tasks []taskResponse
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return tasks, nil
}

func (c *Client) GetTask(id string) (*taskResponse, error) {
	resp, err := c.get("/tasks/" + id)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var t taskResponse
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &t, nil
}

func (c *Client) RetryTask(id string) (*taskResponse, error) {
	resp, err := c.post("/tasks/"+id+"/retry", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var t taskResponse
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &t, nil
}

func (c *Client) GetThread(date string, budget int) (string, error) {
	path := "/thread?"
	if date != "" {
		path += "date=" + date + "&"
	}
	if budget > 0 {
		path += fmt.Sprintf("budget=%d&", budget)
	}
	resp, err := c.get(path)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return "", err
	}
	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return result["thread"], nil
}

func (c *Client) ListAgents() ([]json.RawMessage, error) {
	resp, err := c.get("/agents")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var agents []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return agents, nil
}

func (c *Client) Status() (*statusResponse, error) {
	resp, err := c.get("/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var s statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &s, nil
}

func (c *Client) GetLog(taskID string) ([]json.RawMessage, error) {
	resp, err := c.get("/log/" + taskID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var entries []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return entries, nil
}

func (c *Client) ListSchedule() ([]taskResponse, error) {
	resp, err := c.get("/schedule")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var tasks []taskResponse
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return tasks, nil
}

func (c *Client) RemoveSchedule(id string) (*taskResponse, error) {
	resp, err := c.delete("/schedule/" + id)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var t taskResponse
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &t, nil
}

// HTTP helpers

func (c *Client) get(path string) (*http.Response, error) {
	return c.http.Get("http://wt" + path)
}

func (c *Client) post(path string, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	return c.http.Post("http://wt"+path, "application/json", r)
}

func (c *Client) delete(path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodDelete, "http://wt"+path, nil)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

func checkStatus(resp *http.Response, expected int) error {
	if resp.StatusCode == expected {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, errResp.Error)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
}
