package main

import (
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
)

var (
	mu   sync.Mutex
	held [][]byte
)

func main() {
	addr := ":8080"
	if v := os.Getenv("ADDR"); v != "" {
		addr = v
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/eat", handleEat)
	mux.HandleFunc("/burn", handleBurn)

	log.Printf("listening on %s (pid=%d, uid=%d)", addr, os.Getpid(), os.Getuid())
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.WriteHeader(http.StatusOK)
	log.Println(w, "/eat ok")
}

func handleEat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mbStr := r.URL.Query().Get("mb")
	if mbStr == "" {
		http.Error(w, "missing mb param", http.StatusBadRequest)
		return
	}
	mb, err := strconv.Atoi(mbStr)
	if err != nil || mb <= 0 {
		http.Error(w, "mb must be a positive integer", http.StatusBadRequest)
		return
	}

	block := make([]byte, mb*1024*1024)
	for i := 0; i < len(block); i += 4096 {
		block[i] = 1
	}

	mu.Lock()
	held = append(held, block)
	mu.Unlock()

	log.Printf("/eat: allocated %d MB\n", mb)
}

func handleBurn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	go func() {
		var x uint64
		for {
			x++
			if x == 0 {
				runtime.Gosched()
			}
		}
	}()

	log.Println("/burn: starting CPU burn on 1 core")
}
