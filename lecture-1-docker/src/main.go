package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
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
	mux.HandleFunc("/uname", handleUname)
	mux.HandleFunc("/set-time", handleSetTime)

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
	fmt.Fprintln(w, "ok")
	log.Println("/health ok")
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

func handleUname(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var uts unix.Utsname

	if err := unix.Uname(&uts); err != nil {
		log.Printf("/uname: uname failed: %v", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	log.Printf(
		"/uname: uname succeeded: Domainname=%s Machine=%s Nodename=%s Release=%s Sysname=%s Version=%s",
		uts.Domainname,
		uts.Machine,
		uts.Nodename,
		uts.Release,
		uts.Sysname,
		uts.Version,
	)
}

func handleSetTime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tv := unix.Timeval{
		Sec:  time.Date(2025, 9, 23, 12, 40, 0, 0, time.UTC).Unix(),
		Usec: 0,
	}

	err := unix.Settimeofday(&tv)
	if err != nil {
		log.Printf("/set-time: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprintln(w, "time changed")
}
