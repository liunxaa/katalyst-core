/*
Copyright 2026 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed web/*
var webAssets embed.FS

type apiError struct {
	Error string `json:"error"`
}

func serveWebUI(addr string) error {
	if addr == "" {
		addr = "127.0.0.1:18080"
	}

	assets, err := fs.Sub(webAssets, "web")
	if err != nil {
		return fmt.Errorf("prepare web assets failed: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(assets)))
	mux.HandleFunc("/api/run", handleRunScenario)
	mux.HandleFunc("/api/sample", handleSampleScenario)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("qrm-debugger web UI listening on %s\n", displayURL(addr))
	return server.ListenAndServe()
}

func handleRunScenario(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("read request failed: %v", err))
		return
	}
	defer r.Body.Close()

	var sc scenario
	if err = json.Unmarshal(body, &sc); err != nil {
		writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("parse scenario failed: %v", err))
		return
	}

	result, err := runScenario(r.Context(), sc)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func handleSampleScenario(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var sc scenario
	if err := json.Unmarshal([]byte(sampleScenario()), &sc); err != nil {
		writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("parse sample scenario failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}

func writeAPIError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, apiError{Error: message})
}

func displayURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "http://127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return "http://" + addr
}
