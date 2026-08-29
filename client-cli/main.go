// TitanKV CLI — KV + TitanWiki thin wrappers against the gateway.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	baseURL string
	token   string
)

func main() {
	root := &cobra.Command{
		Use:   "keyforge",
		Short: "TitanKV CLI for KV + TitanWiki",
	}
	root.PersistentFlags().StringVar(&baseURL, "url", getenv("TITANKV_URL", "http://127.0.0.1:8080"), "Gateway base URL")
	root.PersistentFlags().StringVar(&token, "token", os.Getenv("TITANKV_TOKEN"), "Bearer JWT token")

	root.AddCommand(putCmd(), getCmd(), scanCmd(), pingCmd(), wikiCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func pingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ping",
		Short: "Health check",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := http.Get(strings.TrimRight(baseURL, "/") + "/ping")
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			fmt.Println(string(b))
			return nil
		},
	}
}

func putCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "put <key> <value>",
		Short: "Put a KV pair",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _ := json.Marshal(map[string]string{"key": args[0], "value": args[1]})
			return doJSON(http.MethodPost, "/api/data/kv", body)
		},
	}
}

func getCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Get a value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url := fmt.Sprintf("%s/api/data/kv?key=%s", strings.TrimRight(baseURL, "/"), args[0])
			req, _ := http.NewRequest(http.MethodGet, url, nil)
			addAuth(req)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			fmt.Println(string(b))
			return nil
		},
	}
}

func scanCmd() *cobra.Command {
	var start, end string
	c := &cobra.Command{
		Use:   "scan",
		Short: "Scan key range via SSE",
		RunE: func(cmd *cobra.Command, args []string) error {
			url := fmt.Sprintf("%s/api/data/scan?start=%s&end=%s", strings.TrimRight(baseURL, "/"), start, end)
			req, _ := http.NewRequest(http.MethodGet, url, nil)
			addAuth(req)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			io.Copy(os.Stdout, resp.Body)
			return nil
		},
	}
	c.Flags().StringVar(&start, "start", "", "start key (inclusive)")
	c.Flags().StringVar(&end, "end", "", "end key (exclusive)")
	return c
}

// wikiCmd — Phase F2 thin wrappers around TitanWiki HTTP API.
func wikiCmd() *cobra.Command {
	w := &cobra.Command{
		Use:   "wiki",
		Short: "TitanWiki helpers (pages / ask)",
	}
	w.AddCommand(wikiPagesCmd(), wikiAskCmd())
	return w
}

func wikiPagesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pages <collection> [slug]",
		Short: "List wiki index, or get one page by slug",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			col := args[0]
			if len(args) == 1 {
				return doGET(fmt.Sprintf("/api/rag/collections/%s/wiki/index", col))
			}
			return doGET(fmt.Sprintf("/api/rag/collections/%s/wiki/pages/%s", col, args[1]))
		},
	}
}

func wikiAskCmd() *cobra.Command {
	var topK int
	c := &cobra.Command{
		Use:   "ask <collection> <query...>",
		Short: "Wiki-first Q&A (SSE tokens printed to stdout)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			col := args[0]
			query := strings.Join(args[1:], " ")
			body, _ := json.Marshal(map[string]any{"query": query, "top_k": topK})
			url := fmt.Sprintf("%s/api/rag/collections/%s/wiki/ask", strings.TrimRight(baseURL, "/"), col)
			req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")
			addAuth(req)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				b, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
			}
			return streamSSE(resp.Body)
		},
	}
	c.Flags().IntVar(&topK, "top-k", 5, "retrieval top_k")
	return c
}

// streamSSE prints token text to stdout; other events as JSON lines on stderr.
func streamSSE(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var event string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		switch event {
		case "token":
			var m map[string]string
			if json.Unmarshal([]byte(data), &m) == nil {
				fmt.Print(m["text"])
			} else {
				fmt.Print(data)
			}
		case "end":
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, data)
		default:
			if event != "" {
				fmt.Fprintf(os.Stderr, "[%s] %s\n", event, data)
			}
		}
		event = ""
	}
	fmt.Println()
	return scanner.Err()
}

func doGET(path string) error {
	req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
	addAuth(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	fmt.Println(string(b))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func doJSON(method, path string, body []byte) error {
	req, _ := http.NewRequest(method, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addAuth(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	fmt.Println(string(b))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func addAuth(req *http.Request) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
