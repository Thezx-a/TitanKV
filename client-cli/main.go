// TitanKV CLI — keyforge put/get/scan/batch against the gateway or data service.
package main

import (
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
		Short: "TitanKV CLI for KV operations",
	}
	root.PersistentFlags().StringVar(&baseURL, "url", getenv("TITANKV_URL", "http://127.0.0.1:8080"), "Gateway base URL")
	root.PersistentFlags().StringVar(&token, "token", os.Getenv("TITANKV_TOKEN"), "Bearer JWT token")

	root.AddCommand(putCmd(), getCmd(), scanCmd(), pingCmd())
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
