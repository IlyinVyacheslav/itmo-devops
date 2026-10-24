/*
Погодопытный «пожиратель ресурсов» batch для лабы 4.

Заглушка фоновой нагрузки («ночной аналитики»): в бесконечных циклах жрёт
CPU и память, не делая ничего полезного. Нужен, чтобы на нём наглядно
показывать планирование, preemption и eviction под давлением.

Конфигурация через переменные окружения (все необязательные):
  BATCH_CPU_WORKERS    сколько горутин молотят CPU (по умолчанию 2)
  BATCH_MEM_MB         сколько МБ памяти выделить и «потрогать» (по умолчанию 256)
  BATCH_CPU_HOLD_S     пауза между порциями счёта (по умолчанию 0 = без паузы)

Логика простая. Каждая CPU-горутина молотит ядро вхолостую. Горутина памяти
выделяет байтовый буфер заданного размера и читает его страницы — без
реального обращения ОС может не закрепить страницы в физической памяти,
и буфер останется виртуальным, а не resident.
*/
package main

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

func cpuBurn(wg *sync.WaitGroup, id int, hold time.Duration) {
	defer wg.Done()
	i := 0
	for {
		i++
		acc := 0.0
		for j := 0; j < 1000; j++ {
			acc += float64(i) * 3.141592653589793
		}
		_ = acc
		// пассивная пауза, если задана — поделиться ядром с другими горутинами
		if hold > 0 && i%1000 == 0 {
			time.Sleep(hold)
		}
	}
}

func touchMemory(mb int, update time.Duration, wg *sync.WaitGroup) {
	defer wg.Done()
	if mb < 1 {
		mb = 1
	}
	nbytes := int64(mb) * 1024 * 1024
	buf := make([]byte, nbytes) // виртуально; реально закрепляем страницы постепенно
	const block = 4096          // размер страницы
	const chunkSteps = 512      // ~2MB за шаг
	off := int64(0)
	// Крепим страницы чанками с паузой, чтобы RSS рос постепенно: если выделить всё
	// сразу, нода мгновенно уйдёт в kernel-OOM и kubelet не успеет сделать eviction.
	for {
		end := off + int64(chunkSteps*block)
		if end > nbytes {
			end = nbytes
		}
		for ; off < end; off += block {
			buf[off] = 1
		}
		if off >= nbytes {
			break // все страницы закреплены
		}
		time.Sleep(30 * time.Millisecond) // темп набора памяти
	}
	fmt.Printf("[batch] memory: alloc %d MB (resident), held\n", mb)
	// Блокируемся навсегда, держа ссылку на buf, чтобы буфер не ушёл в GC
	// и остался resident. НЕ `select {}` — пустой select рантайм Go считает
	// «навсегда спящей» горутиной и при всех заблокированных горутинах паначит
	// deadlock. `time.Sleep` в цикле пробуждается таймером, поэтому deadlock
	// не триггерит.
	for {
		time.Sleep(time.Hour)
	}
}

func getFloatEnv(name string, def float64) float64 {
	if v, ok := os.LookupEnv(name); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func getIntEnv(name string, def int) int {
	if v, ok := os.LookupEnv(name); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func main() {
	cpuWorkers := getIntEnv("BATCH_CPU_WORKERS", 2)
	memMB := getIntEnv("BATCH_MEM_MB", 256)
	cpuHoldS := getFloatEnv("BATCH_CPU_HOLD_S", 0)

	fmt.Printf("[batch] start cpu_workers=%d mem_mb=%d cpu_hold_s=%.3f\n",
		cpuWorkers, memMB, cpuHoldS)

	hold := time.Duration(cpuHoldS * float64(time.Second))

	var wg sync.WaitGroup
	for id := 0; id < cpuWorkers; id++ {
		wg.Add(1)
		go cpuBurn(&wg, id, hold)
	}

	wg.Add(1)
	go touchMemory(memMB, 0, &wg)

	// Блокируем main навсегда. wg.Wait() тут нельзя: touchMemory никогда не вызовет
	// wg.Done() (он удерживает память вечно), и при cpu_workers=0 рантайм ронял бы
	// программу паникой "all goroutines are asleep - deadlock!". Держим main в
	// `time.Sleep`-цикле: таймер пробуждается => deadlock не триггерится.
	for {
		time.Sleep(time.Hour)
	}
}
