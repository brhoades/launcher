// Command mockkolide stands in for Kolide SaaS so that launcher can be run
// end-to-end locally. It implements the JSONRPC methods that
// pkg/service/client_jsonrpc.go calls, and hands osquery a config plus a set of
// distributed queries so there is real work for the instance to do.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	ID      json.RawMessage `json:"id"`
}

// invented distributed queries -- a mix of osquery core tables and the tables
// that launcher itself serves over the extension socket, so that a failure on
// either side is visible in the results.
var distributedQueries = map[string]string{
	"mock:system_info":      "select hostname, cpu_brand, physical_memory from system_info;",
	"mock:osquery_info":     "select version, pid, watcher, extensions, config_valid from osquery_info;",
	"mock:launcher_info":    "select version, identifier, osquery_instance_id from kolide_launcher_info;",
	"mock:launcher_gc_info": "select * from launcher_gc_info;",
	"mock:users":            "select uid, username, directory from users limit 5;",
	"mock:extensions":       "select uuid, name, version, sdk_version from osquery_extensions;",
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9099", "listen address")
	flag.Parse()

	epoch := strconv.FormatInt(time.Now().Unix(), 10)
	osqueryConfig, err := json.Marshal(map[string]any{
		"options": map[string]any{
			"distributed_interval": 5,
			"schedule_epoch":       epoch,
			"verbose":              true,
		},
		"schedule": map[string]any{
			"mock_scheduled_osquery_info": map[string]any{
				"query":    "select version, watcher, extensions from osquery_info;",
				"interval": 10,
			},
			"mock_scheduled_launcher_info": map[string]any{
				"query":    "select version, identifier from kolide_launcher_info;",
				"interval": 10,
			},
		},
	})
	if err != nil {
		log.Fatalf("marshalling osquery config: %v", err)
	}

	var resultCount atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var result any
		switch req.Method {
		case "RequestEnrollment":
			result = map[string]any{
				"node_key":     "mock-node-key",
				"node_invalid": false,
			}
		case "RequestConfig":
			result = map[string]any{
				"config":       string(osqueryConfig),
				"node_invalid": false,
			}
		case "RequestQueries":
			result = map[string]any{
				"Queries":      map[string]any{"queries": distributedQueries},
				"node_invalid": false,
			}
		case "PublishResults":
			// Log a compact summary so we can see osquery actually answering.
			var params struct {
				Results []struct {
					QueryName string              `json:"query_name"`
					Rows      []map[string]string `json:"rows"`
					Status    int                 `json:"status"`
					Message   string              `json:"message"`
				} `json:"Results"`
			}
			if err := json.Unmarshal(req.Params, &params); err == nil {
				for _, res := range params.Results {
					resultCount.Add(1)
					fmt.Fprintf(os.Stdout, "RESULT %s status=%d msg=%q rows=%d %v\n",
						res.QueryName, res.Status, res.Message, len(res.Rows), res.Rows)
				}
			}
			result = map[string]any{"message": "ok", "node_invalid": false}
		case "PublishLogs":
			result = map[string]any{"message": "ok", "node_invalid": false}
		case "CheckHealth":
			result = map[string]any{"status": 1}
		default:
			log.Printf("unhandled method %q", req.Method)
			result = map[string]any{}
		}

		resultRaw, err := json.Marshal(result)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rpcResponse{ //nolint:errcheck
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  resultRaw,
		})
	})

	log.Printf("mock kolide listening on %s", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
